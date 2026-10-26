package monitor

import (
	"bytes"
	"context"
	"log/slog"
	"openwrt-wifi-guard/internal/devices"
	"sync"
	"testing"
	"time"
)

func TestBootstrapPartialFailureDoesNotSuppressLiveEvents(t *testing.T) {
	f := &fakeBackend{objects: []string{"hostapd.ap0", "hostapd.ap1"}, clients: map[string][]string{
		"hostapd.ap0": {"02:00:00:00:00:01"}, "hostapd.ap1": {"02:00:00:00:00:02"}}, clientFail: map[string]bool{"hostapd.ap1": true}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var log bytes.Buffer
	var mu sync.Mutex
	calls := 0
	initialized := false
	count := 0
	r := devices.New(20)
	runner := Runner{Backend: f, Registry: r, Log: slog.New(slog.NewJSONHandler(&log, nil)), ReconcileInterval: 10 * time.Millisecond, RetryInterval: 5 * time.Millisecond, FlushInterval: 5 * time.Millisecond, Bootstrap: "import", Save: func(s devices.State) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		initialized = s.Initialized
		count = len(s.Devices)
		if calls == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	await(t, func() bool { mu.Lock(); defer mu.Unlock(); return calls >= 2 })
	mu.Lock()
	if !initialized || count != 1 {
		t.Error("initial event window should close even if one AP fails")
	}
	mu.Unlock()
	f.mu.Lock()
	feed := f.feeds[0]
	f.mu.Unlock()
	feed <- Notice{"authorized", "02:00:00:00:00:03", "ap0"}
	await(t, func() bool { mu.Lock(); defer mu.Unlock(); return count == 2 })
	f.mu.Lock()
	f.clientFail["hostapd.ap1"] = false
	f.mu.Unlock()
	await(t, func() bool { mu.Lock(); defer mu.Unlock(); return initialized && count == 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if bytes.Count(log.Bytes(), []byte(`"msg":"new_device"`)) != 1 {
		t.Fatal("only the live post-baseline event should alert", log.String())
	}
	if !bytes.Contains(log.Bytes(), []byte("state_save_failed")) {
		t.Fatal("save error was hidden")
	}
}
