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
	"openwrt-wifi-guard/internal/hostnames"
	"openwrt-wifi-guard/internal/rpc"
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
	Registry         *devices.Registry
	Store            Store
	Save             func(devices.State) error
	Logger           *slog.Logger
	NewSender        func(config.Settings) (Sender, error)
	Commands         chan rpc.Command
	Actions          bool
	AllowBlocking    bool
	PolicySet        func(context.Context, string, bool) (bool, bool, error)
	PolicyStatus     func(string) (bool, bool)
	PolicyReconcile  func(context.Context) error
	ProtectedMACs    []string
	Hostnames        hostnames.Provider
	HostnameInterval time.Duration
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
	commands         chan rpc.Command
	actions          bool
	allowBlocking    bool
	policySet        func(context.Context, string, bool) (bool, bool, error)
	policyStatus     func(string) (bool, bool)
	policyReconcile  func(context.Context) error
	policyQueue      chan policyJob
	protected        map[string]bool

	// Registry-owner-only durability and in-flight policy reservations.
	decisionsUncertain bool
	policyActive       map[string]uint64
	hostnameMu         sync.RWMutex
	hostnameCache      map[string]hostnames.Hint
	hostnameUntil      time.Time
	hostnameWake       chan struct{}
}

var errRequest = errors.New("invalid or unavailable operation")

func New(parent context.Context, o Options) (*Service, error) {
	if o.Registry == nil || o.Store == nil || o.Save == nil {
		return nil, errors.New("application dependencies required")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if (o.Actions || o.PolicySet != nil || o.PolicyReconcile != nil || o.Hostnames != nil) && o.Commands == nil {
		return nil, errors.New("decision owner queue required")
	}
	settings, err := o.Store.Load(parent)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{registry: o.Registry, store: o.Store, save: o.Save, logger: o.Logger, factory: o.NewSender, ctx: ctx, cancel: cancel, configureQueue: make(chan rpc.Command, 16)}
	s.commands, s.actions = o.Commands, o.Actions
	s.allowBlocking = o.AllowBlocking
	s.policySet, s.policyStatus, s.policyReconcile = o.PolicySet, o.PolicyStatus, o.PolicyReconcile
	s.policyQueue = make(chan policyJob, 16)
	s.protected = make(map[string]bool)
	for _, raw := range o.ProtectedMACs {
		mac, err := devices.NormalizeMAC(raw)
		if err != nil {
			cancel()
			return nil, errRequest
		}
		s.protected[mac] = true
	}
	if s.factory == nil {
		s.factory = func(c config.Settings) (Sender, error) {
			return telegram.New(telegram.Options{Token: c.Token, ChatID: c.ChatID, Logger: o.Logger, Callbacks: s.actions, HandleAction: s.actionHandler(c)})
		}
	}
	sender, err := s.prepare(settings)
	if err != nil {
		cancel()
		return nil, err
	}
	s.apply(settings, sender)
	s.wg.Add(1)
	go s.configureLoop()
	if s.policySet != nil || s.policyReconcile != nil {
		initial := make([]policyJob, 0)
		for _, d := range s.registry.State.Devices {
			if d.PolicyRevision != 0 {
				initial = append(initial, policyJob{MAC: d.MAC, Revision: d.PolicyRevision, Deny: d.DesiredDeny})
			}
		}
		s.wg.Add(1)
		go s.policyLoop(initial)
	}
	if o.Hostnames != nil {
		// Persisted hints are not evidence of a still-active DHCP lease.
		s.enrichHostnames()
		s.hostnameWake = make(chan struct{}, 1)
		s.wg.Add(1)
		go s.hostnameLoop(o.Hostnames, o.HostnameInterval)
	}
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
	s.enrichHostnames()
	select {
	case s.hostnameWake <- struct{}{}:
	default:
	}
	s.configMu.RLock()
	sender := s.sender
	s.configMu.RUnlock()
	m := telegram.Message{MAC: e.MAC, At: e.At, Interface: e.Interface}
	if d := s.registry.State.Devices[e.MAC]; d != nil {
		m.Name = d.DisplayName()
	}
	if s.actions && sender != nil {
		if d := s.registry.State.Devices[e.MAC]; d != nil && d.ReviewNonce != 0 {
			m.Keyboard, _ = s.reviewKeyboard(sender, *d)
		}
	}
	if sender != nil && !sender.Enqueue(m) {
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
	if len(c.Method) > 0 && c.Method[0] == '_' {
		if c.Context.Value(decisionContextKey{}) != s {
			c.Reply <- rpc.Result{Err: errRequest}
			return
		}
		switch c.Method {
		case "_hostnames_refresh":
			s.enrichHostnames()
			c.Reply <- rpc.Result{Value: true}
		case "_telegram_action":
			s.enrichHostnames()
			s.startAction(c)
		case "_policy_check":
			value, err := s.checkPolicy(c.Params)
			c.Reply <- rpc.Result{Value: value, Err: err}
		case "_policy_snapshot":
			var value []policyJob
			err := decode(c.Params, &struct{}{})
			if err == nil {
				value, err = s.policySnapshot()
			}
			c.Reply <- rpc.Result{Value: value, Err: err}
		case "_policy_done":
			value, err := s.finishPolicy(c.Params)
			c.Reply <- rpc.Result{Value: value, Err: err}
		default:
			c.Reply <- rpc.Result{Err: errRequest}
		}
		return
	}
	if c.Method == "unblock" {
		s.startUnblock(c)
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
		s.enrichHostnames()
		if decode(data, &struct{}{}) != nil {
			return nil, errRequest
		}
		list := make([]devices.Device, 0, len(s.registry.State.Devices))
		for _, d := range s.registry.State.Devices {
			copy := *d
			if copy.PolicyRevision > 0 && s.policyStatus != nil {
				desired, applied := s.policyStatus(copy.MAC)
				copy.PolicyPending = copy.PolicyPending || desired != copy.DesiredDeny || !applied
			}
			list = append(list, copy)
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
		protected := make([]string, 0, len(s.protected))
		for mac := range s.protected {
			protected = append(protected, mac)
		}
		sort.Strings(protected)
		return struct {
			Enabled          bool     `json:"enabled"`
			ChatID           string   `json:"chat_id"`
			TokenSet         bool     `json:"token_set"`
			CallbacksEnabled bool     `json:"callbacks_enabled"`
			BlockingEnabled  bool     `json:"blocking_enabled"`
			ProtectedMACs    []string `json:"protected_macs"`
		}{s.settings.Enabled, s.settings.ChatID, s.settings.Token != "", s.actions, s.allowBlocking && s.policySet != nil, protected}, nil
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
		if err := s.persistDevice(device, before, history, dirty, fresh); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	default:
		return nil, errRequest
	}
}
