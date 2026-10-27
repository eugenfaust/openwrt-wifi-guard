package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/telegram"
)

type unreadableAfterPublish struct {
	controlledStore
	broken atomic.Bool
}

func (s *unreadableAfterPublish) Load(ctx context.Context) (config.Settings, error) {
	if s.broken.Load() {
		return config.Settings{}, errors.New("storage unavailable")
	}
	return s.controlledStore.Load(ctx)
}
func (s *unreadableAfterPublish) Save(ctx context.Context, c config.Settings) error {
	if err := s.controlledStore.Save(ctx, c); err != nil {
		return err
	}
	s.broken.Store(true)
	return &config.PublishedError{}
}
func TestUnconfirmedConfigurationPausesUntilExplicitRecovery(t *testing.T) {
	store := &unreadableAfterPublish{controlledStore: controlledStore{value: syntheticSettings()}}
	sender := &fakeSender{messages: make(chan telegram.Message, 2)}
	s := newControlledService(t, store, func(config.Settings) (Sender, error) { return sender, nil })
	if _, err := request(s, "configure", `{"enabled":false}`); err == nil {
		t.Fatal("uncertain save reported success")
	}
	if _, err := request(s, "settings", `{}`); err == nil {
		t.Fatal("uncertain configuration exposed as valid")
	}
	if _, err := request(s, "test", `{}`); err == nil {
		t.Fatal("test used uncertain credentials")
	}
	s.Alert(devices.Event{MAC: "02:00:00:00:00:01"})
	select {
	case <-sender.messages:
		t.Fatal("old sender remained active")
	default:
	}
	store.broken.Store(false)
	if _, err := request(s, "configure", `{}`); err != nil {
		t.Fatal("could not recover confirmed disk settings", err)
	}
	if _, err := request(s, "settings", `{}`); err != nil {
		t.Fatal(err)
	}
}
