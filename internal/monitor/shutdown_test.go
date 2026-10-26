package monitor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/devices"
)

type shutdownBackend struct {
	endingBackend
	cancel context.CancelFunc
}

func (s *shutdownBackend) Clients(context.Context, string) ([]string, error) {
	s.cancel()
	return []string{}, nil
}

func TestShutdownDrainsBeforeFinalSave(t *testing.T) {
	r := devices.New(100)
	r.CompleteBootstrap()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	saved := 0
	run := Runner{Backend: &shutdownBackend{cancel: cancel}, Registry: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Bootstrap: "import", ReconcileInterval: time.Second, RetryInterval: time.Second, FlushInterval: time.Second, Save: func(s devices.State) error { saved = len(s.Devices); return nil }}
	if err := run.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if saved != 50 {
		t.Fatalf("shutdown saved %d devices before draining", saved)
	}
}
