package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/telegram"
)

func TestRealNotifierDoesNotBlockOwner(t *testing.T) {
	received := make(chan map[string]any, 1)
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	defer stop()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		received <- data
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer server.Close()
	registry := devices.New(10)
	store := &fakeStore{settings: config.Settings{Enabled: true, ChatID: "12345", Token: "12345:" + strings.Repeat("x", 30)}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, err := New(ctx, Options{Registry: registry, Store: store, Save: func(devices.State) error { return nil }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), NewSender: func(c config.Settings) (Sender, error) {
		return telegram.New(telegram.Options{Token: c.Token, ChatID: c.ChatID, BaseURL: server.URL, Timeout: time.Second})
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	event := devices.Event{MAC: "02:00:00:00:00:01", Interface: "ap0", At: time.Now()}
	service.Alert(event)
	select {
	case body := <-received:
		if body["chat_id"] != "12345" || !strings.Contains(body["text"].(string), event.MAC) {
			t.Fatal("wrong notification payload")
		}
	case <-time.After(time.Second):
		t.Fatal("queued event not delivered")
	}
	start := time.Now()
	if _, err := request(service, "settings", `{}`); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("network blocked owner control operation")
	}
	stop()
}
