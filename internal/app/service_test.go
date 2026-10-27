package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/telegram"
)

type fakeStore struct {
	settings config.Settings
	fail     bool
}

func (f *fakeStore) Load(context.Context) (config.Settings, error) { return f.settings, nil }
func (f *fakeStore) Save(ctx context.Context, s config.Settings) error {
	if f.fail {
		return errors.New("disk full")
	}
	f.settings = s
	return nil
}

type fakeSender struct{ messages chan telegram.Message }

func (f *fakeSender) Run(ctx context.Context)         { <-ctx.Done() }
func (f *fakeSender) Enqueue(m telegram.Message) bool { f.messages <- m; return true }
func (f *fakeSender) SendTest(context.Context) error  { return nil }
func request(s *Service, method, params string) (any, error) {
	reply := make(chan rpc.Result, 1)
	s.Handle(rpc.Command{Context: context.Background(), Method: method, Params: json.RawMessage(params), Reply: reply})
	select {
	case r := <-reply:
		return r.Value, r.Err
	case <-time.After(time.Second):
		return nil, errors.New("request timeout")
	}
}
func setup(t *testing.T, failSave bool) (*Service, *devices.Registry, *fakeStore, *fakeSender) {
	t.Helper()
	r := devices.New(10)
	r.Authorize("02:00:00:00:00:01", "ap0", time.Now())
	r.CompleteBootstrap()
	store := &fakeStore{settings: config.Settings{Enabled: true, ChatID: "12345", Token: "12345:" + strings.Repeat("x", 30)}}
	sender := &fakeSender{messages: make(chan telegram.Message, 5)}
	s, err := New(context.Background(), Options{Registry: r, Store: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Save: func(devices.State) error {
		if failSave {
			return errors.New("disk full")
		}
		return nil
	}, NewSender: func(config.Settings) (Sender, error) { return sender, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, r, store, sender
}
func TestPublicSettingsAndActions(t *testing.T) {
	s, r, store, sender := setup(t, false)
	value, err := request(s, "settings", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), store.settings.Token) || strings.Contains(string(encoded), `"token":`) {
		t.Fatal("token exposed")
	}
	for _, call := range [][2]string{{"rename", `{"mac":"02:00:00:00:00:01","name":"My phone"}`}, {"trust", `{"mac":"02:00:00:00:00:01","trust":"trusted"}`}} {
		if _, err := request(s, call[0], call[1]); err != nil {
			t.Fatal(err)
		}
	}
	if r.State.Devices["02:00:00:00:00:01"].FriendlyName != "My phone" || r.State.Devices["02:00:00:00:00:01"].Trust != "trusted" {
		t.Fatal("mutation lost")
	}
	s.Alert(devices.Event{MAC: "02:00:00:00:00:02", At: time.Now(), Interface: "ap0"})
	select {
	case <-sender.messages:
	case <-time.After(time.Second):
		t.Fatal("notification not queued")
	}
	if _, err := request(s, "test", `{}`); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidOrFailedMutationRollsBack(t *testing.T) {
	s, r, _, _ := setup(t, true)
	for _, call := range [][2]string{{"rename", `{"mac":"02:00:00:00:00:01","name":"Changed"}`}, {"trust", `{"mac":"02:00:00:00:00:01","trust":"blocked"}`}, {"configure", `{"enabled":true,"chat_id":"$(touch evil)"}`}, {"settings", `{"unexpected":"field"}`}} {
		if _, err := request(s, call[0], call[1]); err == nil {
			t.Fatal("invalid request accepted", call[0])
		}
	}
	if r.State.Devices["02:00:00:00:00:01"].FriendlyName != "" {
		t.Fatal("failed rename remained in memory")
	}
}
func TestConfigureBlankKeepsTokenAndClearRequiresDisable(t *testing.T) {
	s, _, store, _ := setup(t, false)
	original := store.settings.Token
	if _, err := request(s, "configure", `{"enabled":true,"chat_id":"12345","token":""}`); err != nil {
		t.Fatal(err)
	}
	if store.settings.Token != original {
		t.Fatal("blank token erased secret")
	}
	if _, err := request(s, "configure", `{"clear_token":true}`); err == nil {
		t.Fatal("enabled without token accepted")
	}
	if _, err := request(s, "configure", `{"enabled":false,"clear_token":true}`); err != nil {
		t.Fatal(err)
	}
	if store.settings.Token != "" || store.settings.Enabled {
		t.Fatal("clear failed")
	}
}
