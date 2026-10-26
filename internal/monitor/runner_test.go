package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"openwrt-wifi-guard/internal/devices"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu         sync.Mutex
	objects    []string
	clients    map[string][]string
	fail       bool
	clientFail map[string]bool
	subs       int
	feeds      []chan Notice
	ends       []chan error
}

func (f *fakeBackend) Discover(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("ubus unavailable")
	}
	return append([]string{}, f.objects...), nil
}
func (f *fakeBackend) Clients(ctx context.Context, obj string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clientFail[obj] {
		return nil, errors.New("AP snapshot unavailable")
	}
	return append([]string{}, f.clients[obj]...), nil
}
func (f *fakeBackend) Subscribe(ctx context.Context, objects []string) (<-chan Notice, <-chan error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs++
	e := make(chan Notice, 10)
	d := make(chan error, 1)
	f.feeds = append(f.feeds, e)
	f.ends = append(f.ends, d)
	go func() { <-ctx.Done(); close(e); close(d) }()
	return e, d
}
func await(t *testing.T, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out")
}
func TestRunnerRestartDiscoveryAndMissedEvents(t *testing.T) {
	f := &fakeBackend{objects: []string{"hostapd.ap0"}, clients: map[string][]string{"hostapd.ap0": {"02:00:00:00:00:01"}}}
	r := devices.New(30)
	var mu sync.Mutex
	var saved devices.State
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := Runner{Backend: f, Registry: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ReconcileInterval: 10 * time.Millisecond, RetryInterval: 5 * time.Millisecond, FlushInterval: 5 * time.Millisecond, Bootstrap: "import", Save: func(s devices.State) error {
		// Save receives live state; copy only values under observation, not map pointers.
		copy := devices.State{Initialized: s.Initialized, Devices: map[string]*devices.Device{}}
		for k, v := range s.Devices {
			d := *v
			copy.Devices[k] = &d
		}
		mu.Lock()
		saved = copy
		mu.Unlock()
		return nil
	}}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	await(t, func() bool { mu.Lock(); defer mu.Unlock(); return saved.Initialized && len(saved.Devices) == 1 })
	// An event recovers immediately; repeated authorization and roaming do not rediscover identity.
	f.mu.Lock()
	feed := f.feeds[0]
	f.mu.Unlock()
	feed <- Notice{"authorized", "02:00:00:00:00:02", "ap0"}
	feed <- Notice{"authorized", "02:00:00:00:00:02", "ap0"}
	await(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(saved.Devices) == 2 })
	// Simulate ubus subscription exit and ensure it is restarted.
	f.mu.Lock()
	f.ends[0] <- errors.New("ubus restarted")
	f.mu.Unlock()
	await(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.subs >= 2 })
	// Replace the AP; a missed authorization is recovered by its snapshot.
	f.mu.Lock()
	f.objects = []string{"hostapd.ap1"}
	f.clients = map[string][]string{"hostapd.ap1": {"02:00:00:00:00:03"}}
	f.mu.Unlock()
	await(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		d := saved.Devices["02:00:00:00:00:03"]
		old := saved.Devices["02:00:00:00:00:01"]
		return d != nil && d.Online && old != nil && !old.Online
	})
	// A successful empty discovery means Wi-Fi is disabled, not a failed query.
	f.mu.Lock()
	f.objects = nil
	f.mu.Unlock()
	await(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		if len(saved.Devices) != 3 {
			return false
		}
		for _, d := range saved.Devices {
			if d.Online {
				return false
			}
		}
		return true
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown hung")
	}
	if len(r.State.Devices) != 3 {
		t.Fatal("identity lost")
	}
	n := 0
	for _, e := range r.State.History {
		if e.Kind == "discovered" {
			n++
		}
	}
	if n != 3 {
		t.Fatal("duplicate discoveries", n)
	}
}
func TestRunnerCorruptIntervals(t *testing.T) {
	r := Runner{}
	if r.Run(context.Background()) == nil {
		t.Fatal("invalid config accepted")
	}
}
func TestRunnerShutdownSaveFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeBackend{clients: map[string][]string{}}
	r := Runner{Backend: f, Registry: devices.New(10), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ReconcileInterval: time.Second, RetryInterval: time.Second, FlushInterval: time.Second, Bootstrap: "notify", Save: func(devices.State) error { return errors.New("disk full") }}
	if err := r.Run(ctx); err == nil {
		t.Fatal("save failure hidden")
	}
}
