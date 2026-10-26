package monitor

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"openwrt-wifi-guard/internal/devices"
	"sync"
	"testing"
	"time"
)

// This backend queues an authorization during baseline collection, before the
// caller can finish bootstrap. The client has left by the time the snapshot runs.
type queuedBackend struct {
	mu     sync.Mutex
	events chan Notice
	done   chan error
	calls  int
}

func (q *queuedBackend) Discover(context.Context) ([]string, error) {
	return []string{"hostapd.ap0"}, nil
}
func (q *queuedBackend) Clients(context.Context, string) ([]string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	if q.calls == 1 {
		q.events <- Notice{"authorized", "02:00:00:00:00:01", "ap0"}
	}
	return []string{}, nil
}
func (q *queuedBackend) Subscribe(ctx context.Context, _ []string) (<-chan Notice, <-chan error) {
	q.events = make(chan Notice, 128)
	q.done = make(chan error, 1)
	go func() { <-ctx.Done(); q.mu.Lock(); defer q.mu.Unlock(); close(q.events); close(q.done) }()
	return q.events, q.done
}
func TestQueuedBootstrapEventIsSilent(t *testing.T) {
	q := &queuedBackend{}
	r := devices.New(10)
	var log bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	run := Runner{Backend: q, Registry: r, Log: slog.New(slog.NewJSONHandler(&log, nil)), Bootstrap: "import", ReconcileInterval: time.Second, RetryInterval: time.Second, FlushInterval: time.Second, Save: func(devices.State) error { return nil }}
	if err := run.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.State.Devices) != 1 {
		t.Fatal("initial event lost")
	}
	if bytes.Contains(log.Bytes(), []byte(`"msg":"new_device"`)) {
		t.Fatal("initial event falsely alerted")
	}
}

type endingBackend struct{ emitted bool }

func (e *endingBackend) Discover(context.Context) ([]string, error) {
	return []string{"hostapd.ap0"}, nil
}
func (e *endingBackend) Clients(context.Context, string) ([]string, error) { return []string{}, nil }
func (e *endingBackend) Subscribe(context.Context, []string) (<-chan Notice, <-chan error) {
	c := make(chan Notice, 128)
	d := make(chan error, 1)
	if !e.emitted {
		e.emitted = true
		for n := 0; n < 50; n++ {
			c <- Notice{"authorized", fmtMAC(n), "ap0"}
		}
	}
	close(c)
	d <- io.EOF
	close(d)
	return c, d
}
func fmtMAC(n int) string {
	const hex = "0123456789abcdef"
	return "02:00:00:00:00:" + string([]byte{hex[n/16], hex[n%16]})
}
func TestSubscriptionEndDrainsAcceptedEvents(t *testing.T) {
	for n := 0; n < 20; n++ {
		r := devices.New(100)
		r.CompleteBootstrap()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		run := Runner{Backend: &endingBackend{}, Registry: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Bootstrap: "import", ReconcileInterval: time.Second, RetryInterval: time.Second, FlushInterval: time.Second, Save: func(devices.State) error { return nil }}
		err := run.Run(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if len(r.State.Devices) != 50 {
			t.Fatalf("dropped accepted events: got %d", len(r.State.Devices))
		}
	}
}
