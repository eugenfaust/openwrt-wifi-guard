// wifi-guard monitors authorized Wi-Fi clients without opening network ports.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"openwrt-wifi-guard/internal/app"
	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/monitor"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/storage"
	"openwrt-wifi-guard/internal/telegram"
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
	if len(os.Args) > 1 && os.Args[1] == "rpc" {
		return runRPC(os.Args[2:])
	}
	state := flag.String("state", "/etc/wifi-guard/state.json", "Persistent registry path (sensitive device data)")
	ubus := flag.String("ubus", "ubus", "ubus executable path")
	uci := flag.String("uci", "uci", "uci executable path")
	uciDir := flag.String("uci-dir", "/etc/config", "Private UCI configuration directory")
	socket := flag.String("socket", rpc.DefaultSocket, "Root-private Unix socket; empty disables RPC")
	testTelegram := flag.Bool("test-telegram", false, "Send a labeled Telegram test using restricted UCI settings, then exit")
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
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	store := config.Store{Dir: *uciDir, UCI: *uci, Timeout: *timeout}
	if *testTelegram {
		c, err := store.Load(ctx)
		if err != nil {
			return err
		}
		n, err := telegram.New(telegram.Options{Token: c.Token, ChatID: c.ChatID, Logger: log})
		if err != nil {
			return err
		}
		testCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		defer cancel()
		if err := n.SendTest(testCtx); err != nil {
			return err
		}
		log.Info("telegram_test_accepted")
		return nil
	}
	if !*dump {
		lock, err := storage.AcquireLock(*state)
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	r, err := storage.Load(*state, *history)
	if err != nil {
		return err
	}
	if *dump {
		return json.NewEncoder(os.Stdout).Encode(r.State)
	}
	save := func(s devices.State) error { return storage.Save(*state, s) }
	service, err := app.New(ctx, app.Options{Registry: r, Store: store, Save: save, Logger: log})
	if err != nil {
		return err
	}
	defer service.Close()
	var commands chan rpc.Command
	var serving chan error
	if *socket != "" {
		commands = make(chan rpc.Command, 16)
		serving = make(chan error, 1)
		go func() {
			err := rpc.Serve(ctx, *socket, rpc.Dispatch(commands))
			serving <- err
			if err != nil {
				cancel()
			}
		}()
	}
	runner := monitor.Runner{Backend: monitor.Ubus{Path: *ubus, Timeout: *timeout}, Registry: r, Log: log, ReconcileInterval: *interval, FlushInterval: *flush, CheckpointInterval: *checkpoint, ResubscribeInterval: *renew, RetryInterval: *retry, Bootstrap: *bootstrap, Save: save, OnNewDevice: service.Alert, Commands: commands, HandleCommand: service.Handle}
	runErr := runner.Run(ctx)
	cancel()
	if serving != nil {
		if err := <-serving; err != nil {
			return err
		}
	}
	return runErr
}
