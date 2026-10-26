// wifi-guard monitors authorized Wi-Fi clients without opening network ports.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/monitor"
	"openwrt-wifi-guard/internal/storage"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}
func run() error {
	state := flag.String("state", "/etc/wifi-guard/state.json", "Persistent registry path (sensitive device data)")
	ubus := flag.String("ubus", "ubus", "ubus executable path")
	interval := flag.Duration("reconcile", 60*time.Second, "AP rediscovery and client reconciliation interval")
	flush := flag.Duration("flush", 30*time.Second, "Critical state flush interval")
	checkpoint := flag.Duration("checkpoint", 15*time.Minute, "Last-seen-only persistence interval")
	renew := flag.Duration("resubscribe", 5*time.Minute, "Periodically renew subscriptions even if object names are unchanged")
	retry := flag.Duration("retry", 5*time.Second, "Subscription reconnect interval")
	timeout := flag.Duration("command-timeout", 5*time.Second, "Timeout for each ubus list/call (not idle subscribe)")
	bootstrap := flag.String("bootstrap", "import", "First-run policy: import (silent) or notify")
	history := flag.Int("history-limit", 500, "Maximum persisted lifecycle events")
	dump := flag.Bool("dump", false, "Print registry JSON and exit without ubus calls")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *history < 1 || *history > 10000 || *interval <= 0 || *flush <= 0 || *checkpoint <= 0 || *retry <= 0 || *renew <= 0 || *timeout <= 0 {
		return fmt.Errorf("invalid interval or history limit")
	}
	if *bootstrap != "import" && *bootstrap != "notify" {
		return fmt.Errorf("bootstrap must be import or notify")
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)
	r, err := storage.Load(*state, *history)
	if err != nil {
		return err
	}
	if *dump {
		return json.NewEncoder(os.Stdout).Encode(r.State)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	runner := monitor.Runner{Backend: monitor.Ubus{Path: *ubus, Timeout: *timeout}, Registry: r, Log: log, ReconcileInterval: *interval, FlushInterval: *flush, CheckpointInterval: *checkpoint, ResubscribeInterval: *renew, RetryInterval: *retry, Bootstrap: *bootstrap, Save: func(s devices.State) error { return storage.Save(*state, s) }}
	return runner.Run(ctx)
}
