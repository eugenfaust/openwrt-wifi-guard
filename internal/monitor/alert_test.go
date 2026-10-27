package monitor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/devices"
)

func TestBeforeAlertDurabilityOrdering(t *testing.T) {
	for _, mode := range []string{"success", "save_failure", "prepare_failure", "nil_hook"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r := devices.New(10)
			f := &fakeBackend{objects: []string{"hostapd.ap0"}, clients: map[string][]string{"hostapd.ap0": {"02:00:00:00:00:01"}}}
			var order []string
			var logs bytes.Buffer
			runner := Runner{Registry: r, Backend: f, Log: slog.New(slog.NewTextHandler(&logs, nil)), Bootstrap: "notify", ReconcileInterval: time.Hour, FlushInterval: time.Hour, RetryInterval: time.Hour}
			runner.BeforeAlert = func(e devices.Event) error {
				order = append(order, "prepare")
				if mode == "prepare_failure" {
					cancel()
					return errors.New("secret preparation detail")
				}
				return r.EnsureReview(e.MAC, strings.Repeat("a", 64), e.At)
			}
			if mode == "nil_hook" {
				runner.BeforeAlert = nil
			}
			runner.Save = func(s devices.State) error {
				order = append(order, "save")
				if !r.Dirty {
					t.Error("dirty state cleared before save succeeded")
				}
				if mode != "nil_hook" && mode != "prepare_failure" && s.Devices["02:00:00:00:00:01"].ReviewNonce == 0 {
					t.Error("saved before preparation")
				}
				if mode == "save_failure" {
					cancel()
					return errors.New("secret disk detail")
				}
				return nil
			}
			runner.OnNewDevice = func(devices.Event) {
				order = append(order, "alert")
				if mode == "success" && r.Dirty {
					t.Error("alert published before durable save")
				}
				cancel()
			}
			err := runner.Run(ctx)
			if mode == "save_failure" {
				if err == nil || !r.Dirty {
					t.Fatal("failed save claimed durability")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var want []string
			switch mode {
			case "success":
				want = []string{"prepare", "save", "alert"}
			case "save_failure":
				want = []string{"prepare", "save", "save"}
			case "prepare_failure":
				want = []string{"prepare", "save"} // only the normal shutdown checkpoint
			case "nil_hook":
				want = []string{"alert", "save"} // legacy callback still precedes checkpoint
			}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("order %v, want %v", order, want)
			}
			// The shutdown save path predates this hook and logs no error; the
			// preparation/alert failure logs must never include callback details.
			if strings.Contains(logs.String(), "secret") {
				t.Fatal("sensitive callback error was logged")
			}
		})
	}
}
