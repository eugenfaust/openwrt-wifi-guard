package monitor

import (
	"context"
	"io"
	"log/slog"
	"openwrt-wifi-guard/internal/devices"
	"testing"
	"time"
)

func TestSubscriptionRenewsWithUnchangedNames(t *testing.T) {
	f := &fakeBackend{objects: []string{"hostapd.ap0"}, clients: map[string][]string{"hostapd.ap0": {}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := Runner{Backend: f, Registry: devices.New(5), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Bootstrap: "import", ReconcileInterval: 5 * time.Millisecond, RetryInterval: time.Second, FlushInterval: time.Second, ResubscribeInterval: 10 * time.Millisecond, Save: func(devices.State) error { return nil }}
	done := make(chan error, 1)
	go func() { done <- run.Run(ctx) }()
	await(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.subs >= 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
