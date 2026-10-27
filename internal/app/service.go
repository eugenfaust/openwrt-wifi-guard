// Package app keeps registry commands on the monitor owner and configuration
// I/O on a separate bounded, serialized worker.
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/storage"
	"openwrt-wifi-guard/internal/telegram"
)

type Store interface {
	Load(context.Context) (config.Settings, error)
	Save(context.Context, config.Settings) error
}
type Sender interface {
	Run(context.Context)
	Enqueue(telegram.Message) bool
	SendTest(context.Context) error
}
type Options struct {
	Registry  *devices.Registry
	Store     Store
	Save      func(devices.State) error
	Logger    *slog.Logger
	NewSender func(config.Settings) (Sender, error)
}
type Service struct {
	registry         *devices.Registry
	store            Store
	save             func(devices.State) error
	logger           *slog.Logger
	settings         config.Settings
	factory          func(config.Settings) (Sender, error)
	sender           Sender
	cancelGeneration context.CancelFunc
	generation       context.Context
	testSender       Sender
	configUncertain  bool
	configMu         sync.RWMutex
	configureQueue   chan rpc.Command
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
}

var errRequest = errors.New("invalid or unavailable operation")

func New(parent context.Context, o Options) (*Service, error) {
	if o.Registry == nil || o.Store == nil || o.Save == nil {
		return nil, errors.New("application dependencies required")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.NewSender == nil {
		o.NewSender = func(c config.Settings) (Sender, error) {
			return telegram.New(telegram.Options{Token: c.Token, ChatID: c.ChatID, Logger: o.Logger})
		}
	}
	settings, err := o.Store.Load(parent)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{registry: o.Registry, store: o.Store, save: o.Save, logger: o.Logger, factory: o.NewSender, ctx: ctx, cancel: cancel, configureQueue: make(chan rpc.Command, 16)}
	sender, err := s.prepare(settings)
	if err != nil {
		cancel()
		return nil, err
	}
	s.apply(settings, sender)
	s.wg.Add(1)
	go s.configureLoop()
	return s, nil
}
func (s *Service) prepare(c config.Settings) (Sender, error) {
	if err := config.Validate(c); err != nil {
		return nil, errRequest
	}
	if !c.Enabled {
		return nil, nil
	}
	return s.factory(c)
}
func (s *Service) apply(c config.Settings, n Sender) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	// An unchanged effective configuration must not discard accepted work.
	if s.generation != nil && !s.configUncertain && s.settings == c {
		return
	}
	if s.cancelGeneration != nil {
		s.cancelGeneration()
	}
	s.generation, s.cancelGeneration = context.WithCancel(s.ctx)
	s.settings = c
	s.sender = n
	s.testSender = n
	s.configUncertain = false
	if n != nil && s.ctx.Err() == nil {
		ctx := s.generation
		s.wg.Add(1)
		go func() { defer s.wg.Done(); n.Run(ctx) }()
	}
}

// Close is called only after the registry owner loop has stopped.
func (s *Service) Close() { s.cancel(); s.wg.Wait() }
func (s *Service) Alert(e devices.Event) {
	s.configMu.RLock()
	sender := s.sender
	s.configMu.RUnlock()
	if sender != nil && !sender.Enqueue(telegram.Message{MAC: e.MAC, At: e.At, Interface: e.Interface}) {
		s.logger.Warn("telegram_queue_full_or_stopped")
	}
}
func decode(data json.RawMessage, target any) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	if data[0] != '{' {
		return errRequest
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return errRequest
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errRequest
	}
	return nil
}

// Handle runs only on the monitor owner goroutine. Configuration I/O and network tests run separately;
// every reply channel is buffered, so a timed-out caller cannot stall monitoring.
func (s *Service) Handle(c rpc.Command) {
	if c.Context.Err() != nil {
		return
	}
	if c.Method == "configure" {
		select {
		case s.configureQueue <- c:
		case <-c.Context.Done():
		default:
			c.Reply <- rpc.Result{Err: errRequest}
		}
		return
	}
	if c.Method == "test" {
		s.startTest(c)
		return
	}
	value, err := s.handle(c.Context, c.Method, c.Params)
	c.Reply <- rpc.Result{Value: value, Err: err}
}
func (s *Service) handle(ctx context.Context, method string, data json.RawMessage) (any, error) {
	switch method {
	case "devices":
		if decode(data, &struct{}{}) != nil {
			return nil, errRequest
		}
		list := make([]devices.Device, 0, len(s.registry.State.Devices))
		for _, d := range s.registry.State.Devices {
			list = append(list, *d)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].MAC < list[j].MAC })
		return map[string]any{"devices": list}, nil
	case "history":
		var p struct {
			Limit int `json:"limit"`
		}
		if decode(data, &p) != nil {
			return nil, errRequest
		}
		if p.Limit == 0 {
			p.Limit = 100
		}
		if p.Limit < 1 || p.Limit > 500 {
			return nil, errRequest
		}
		events := make([]devices.Event, 0, p.Limit)
		h := s.registry.State.History
		for n := len(h) - 1; n >= 0 && len(events) < p.Limit; n-- {
			events = append(events, h[n])
		}
		return map[string]any{"events": events}, nil
	case "settings":
		if decode(data, &struct{}{}) != nil {
			return nil, errRequest
		}
		s.configMu.RLock()
		defer s.configMu.RUnlock()
		if s.configUncertain {
			return nil, errRequest
		}
		return struct {
			Enabled  bool   `json:"enabled"`
			ChatID   string `json:"chat_id"`
			TokenSet bool   `json:"token_set"`
		}{s.settings.Enabled, s.settings.ChatID, s.settings.Token != ""}, nil
	case "rename", "trust":
		var mac string
		var apply func() error
		if method == "rename" {
			var p struct {
				MAC  string `json:"mac"`
				Name string `json:"name"`
			}
			if decode(data, &p) != nil {
				return nil, errRequest
			}
			mac = p.MAC
			apply = func() error { return s.registry.Rename(mac, p.Name, time.Now().UTC()) }
		} else {
			var p struct {
				MAC   string `json:"mac"`
				Trust string `json:"trust"`
			}
			if decode(data, &p) != nil {
				return nil, errRequest
			}
			mac = p.MAC
			apply = func() error { return s.registry.SetTrust(mac, p.Trust, time.Now().UTC()) }
		}
		normalized, err := devices.NormalizeMAC(mac)
		if err != nil {
			return nil, errRequest
		}
		device := s.registry.State.Devices[normalized]
		if device == nil {
			return nil, errRequest
		}
		before := *device
		history := append([]devices.Event(nil), s.registry.State.History...)
		dirty, fresh := s.registry.Dirty, s.registry.Fresh
		if err := apply(); err != nil {
			return nil, errRequest
		}
		if err := s.save(s.registry.State); err != nil {
			var published *storage.PublishedError
			if errors.As(err, &published) {
				s.registry.Dirty = true
				s.logger.Error("device_update_durability_unconfirmed")
				return nil, errRequest
			}
			*device = before
			s.registry.State.History = history
			s.registry.Dirty = dirty
			s.registry.Fresh = fresh
			s.logger.Error("device_update_persistence_failed")
			return nil, errRequest
		}
		s.registry.Dirty = false
		s.registry.Fresh = false
		return map[string]bool{"ok": true}, nil
	default:
		return nil, errRequest
	}
}
