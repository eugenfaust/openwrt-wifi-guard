package monitor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
	"testing"
	"time"
)

func TestNewDeviceCallbackAndOwnerCommands(t *testing.T) {
	f := &fakeBackend{objects: []string{"hostapd.ap0"}, clients: map[string][]string{"hostapd.ap0": {"02:00:00:00:00:01"}}}
	queue := make(chan rpc.Command, 2)
	alerts := make(chan devices.Event, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := Runner{Backend: f, Registry: devices.New(10), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Bootstrap: "notify", ReconcileInterval: time.Second, RetryInterval: time.Second, FlushInterval: time.Second, OnNewDevice: func(e devices.Event) { alerts <- e }, Commands: queue, HandleCommand: func(c rpc.Command) { c.Reply <- rpc.Result{Value: map[string]bool{"running": true}} }, Save: func(devices.State) error { return nil }}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	select {
	case <-alerts:
	case <-time.After(time.Second):
		t.Fatal("alert hook not called")
	}
	callCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	value, err := rpc.Dispatch(queue)(callCtx, "devices", json.RawMessage(`{}`))
	if err != nil || value == nil {
		t.Fatal(value, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
