package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/telegram"
)

type controlledStore struct {
	mu               sync.Mutex
	value            config.Settings
	started, release chan struct{}
	publishAndCancel context.CancelFunc
	saves            int
}

func (s *controlledStore) Load(ctx context.Context) (config.Settings, error) {
	if ctx.Err() != nil {
		return config.Settings{}, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, nil
}
func (s *controlledStore) Save(ctx context.Context, c config.Settings) error {
	s.mu.Lock()
	s.saves++
	s.mu.Unlock()
	if s.started != nil {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.value = c
	s.mu.Unlock()
	if s.publishAndCancel != nil {
		s.publishAndCancel()
		return &config.PublishedError{}
	}
	return nil
}
func newControlledService(t *testing.T, store Store, factory func(config.Settings) (Sender, error)) *Service {
	t.Helper()
	s, err := New(context.Background(), Options{Registry: devices.New(10), Store: store, Save: func(devices.State) error { return nil }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), NewSender: factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func syntheticSettings() config.Settings {
	return config.Settings{Enabled: true, ChatID: "12345", Token: "12345:" + strings.Repeat("x", 30)}
}
func command(ctx context.Context, method, params string) rpc.Command {
	return rpc.Command{Context: ctx, Method: method, Params: json.RawMessage(params), Reply: make(chan rpc.Result, 1)}
}

func TestConfigureDoesNotBlockMonitorOwner(t *testing.T) {
	store := &controlledStore{value: syntheticSettings(), started: make(chan struct{}), release: make(chan struct{})}
	sender := &fakeSender{messages: make(chan telegram.Message, 5)}
	s := newControlledService(t, store, func(config.Settings) (Sender, error) { return sender, nil })
	defer close(store.release)
	c := command(context.Background(), "configure", `{"chat_id":"54321"}`)
	returned := make(chan struct{})
	go func() { s.Handle(c); close(returned) }()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("save not started")
	}
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("UCI save blocks monitor owner")
	}
	s.Alert(devices.Event{MAC: "02:00:00:00:00:01", Interface: "ap0", At: time.Now()})
	select {
	case <-sender.messages:
	case <-time.After(time.Second):
		t.Fatal("alert stalled behind UCI")
	}
}
func TestNoopConfigureKeepsSenderAndDoesNotWrite(t *testing.T) {
	store := &controlledStore{value: syntheticSettings()}
	var factories atomic.Int32
	sender := &fakeSender{messages: make(chan telegram.Message, 5)}
	s := newControlledService(t, store, func(config.Settings) (Sender, error) { factories.Add(1); return sender, nil })
	if _, err := request(s, "configure", `{"enabled":true,"chat_id":"12345","token":""}`); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	saves := store.saves
	store.mu.Unlock()
	if factories.Load() != 1 || saves != 0 {
		t.Fatalf("no-op reset notifier or wrote flash: factories=%d saves=%d", factories.Load(), saves)
	}
}
func TestCanceledPostPublishSaveAlignsLiveConfiguration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &controlledStore{value: syntheticSettings(), publishAndCancel: cancel}
	sender := &fakeSender{messages: make(chan telegram.Message, 5)}
	s := newControlledService(t, store, func(config.Settings) (Sender, error) { return sender, nil })
	c := command(ctx, "configure", `{"enabled":false}`)
	s.Handle(c)
	select {
	case result := <-c.Reply:
		if result.Err == nil {
			t.Fatal("durability uncertainty reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("save did not finish")
	}
	value, err := request(s, "settings", `{}`)
	if err != nil {
		t.Fatal("published settings not recovered", err)
	}
	encoded, _ := json.Marshal(value)
	var actual struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.Unmarshal(encoded, &actual)
	if actual.Enabled {
		t.Fatal("old notifier remains enabled after published disable")
	}
	s.Alert(devices.Event{MAC: "02:00:00:00:00:01"})
	select {
	case <-sender.messages:
		t.Fatal("old sender accepted after published disable")
	default:
	}
}

type cancelAwareSender struct{ tests chan context.Context }

func (s *cancelAwareSender) Run(ctx context.Context)       { <-ctx.Done() }
func (s *cancelAwareSender) Enqueue(telegram.Message) bool { return true }
func (s *cancelAwareSender) SendTest(ctx context.Context) error {
	s.tests <- ctx
	<-ctx.Done()
	return ctx.Err()
}
func TestCredentialGenerationCancelsInflightTest(t *testing.T) {
	store := &controlledStore{value: syntheticSettings()}
	sender := &cancelAwareSender{tests: make(chan context.Context, 2)}
	s := newControlledService(t, store, func(config.Settings) (Sender, error) { return sender, nil })
	c := command(context.Background(), "test", `{}`)
	s.Handle(c)
	select {
	case <-sender.tests:
	case <-time.After(time.Second):
		t.Fatal("test not started")
	}
	if _, err := request(s, "configure", `{"enabled":false,"clear_token":true}`); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-c.Reply:
		if result.Err == nil {
			t.Fatal("canceled test succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("old-credential test survives clear/disable")
	}
}
