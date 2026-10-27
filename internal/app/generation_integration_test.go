package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/telegram"
)

func TestTestsAndAlertsShareCredentialGenerationGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			var active, peak, calls, factories atomic.Int32
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			entered := make(chan struct{}, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				current := active.Add(1)
				defer active.Add(-1)
				for {
					old := peak.Load()
					if current <= old || peak.CompareAndSwap(old, current) {
						break
					}
				}
				calls.Add(1)
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
			}))
			defer server.Close()
			defer unblock()
			initial := syntheticSettings()
			initial.Enabled = enabled
			store := &controlledStore{value: initial}
			s := newControlledService(t, store, func(c config.Settings) (Sender, error) {
				factories.Add(1)
				return telegram.New(telegram.Options{Token: c.Token, ChatID: c.ChatID, BaseURL: server.URL, Timeout: time.Second, MaxAttempts: 1})
			})
			if enabled {
				s.Alert(devices.Event{MAC: "02:00:00:00:00:01", At: time.Now(), Interface: "ap0"})
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("normal notification not started")
				}
			}
			c1 := command(context.Background(), "test", `{}`)
			s.Handle(c1)
			if !enabled {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("first test not started")
				}
			}
			c2 := command(context.Background(), "test", `{}`)
			s.Handle(c2)
			time.Sleep(30 * time.Millisecond)
			if peak.Load() != 1 || factories.Load() != 1 {
				t.Fatalf("tests escaped shared sender: concurrent=%d factories=%d", peak.Load(), factories.Load())
			}
			unblock()
			for _, reply := range []chan rpc.Result{c1.Reply, c2.Reply} {
				select {
				case result := <-reply:
					if result.Err != nil {
						t.Fatal(result.Err)
					}
				case <-time.After(time.Second):
					t.Fatal("serialized test did not finish")
				}
			}
			expected := int32(2)
			if enabled {
				expected++
			}
			if calls.Load() != expected {
				t.Fatalf("calls=%d expected=%d", calls.Load(), expected)
			}
		})
	}
}
