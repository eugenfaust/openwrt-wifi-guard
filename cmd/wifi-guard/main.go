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
	"openwrt-wifi-guard/internal/hostnames"
	"openwrt-wifi-guard/internal/monitor"
	"openwrt-wifi-guard/internal/policy"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/storage"
	"openwrt-wifi-guard/internal/telegram"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	actions := flag.Bool("telegram-actions", false, "Enable exclusive private-chat callback polling (requires a dedicated bot)")
	blocking := flag.Bool("allow-blocking", false, "Allow confirmed permanent MAC deny; requires protected administrative MACs")
	protectedRaw := flag.String("protected-macs", "", "Comma-separated administrative MACs that must never be blocked")
	policyPath := flag.String("policy-state", "", "Policy journal; default policy.json adjacent to registry")
	controlDir := flag.String("hostapd-control-dir", "/var/run/hostapd", "Hostapd Unix control directory")
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
	protected, err := protectedMACs(*protectedRaw)
	if err != nil {
		return err
	}
	if *blocking && (!*actions || len(protected) == 0) {
		return fmt.Errorf("blocking requires Telegram actions and protected administrative MACs")
	}
	if *policyPath == "" {
		*policyPath = filepath.Join(filepath.Dir(*state), "policy.json")
	}
	commands := make(chan rpc.Command, 16)
	options := app.Options{Registry: r, Store: store, Save: save, Logger: log, Commands: commands, Actions: *actions, AllowBlocking: *blocking, ProtectedMACs: protected, Hostnames: hostnames.Local{UCI: *uci, ConfigDir: *uciDir, Timeout: *timeout}}
	_, policyErr := os.Lstat(*policyPath)
	if *blocking || policyErr == nil {
		engine, err := policy.Open(policy.Options{Path: *policyPath, ConfigDir: *uciDir, UCI: *uci, UBus: *ubus, ControlDir: *controlDir, ProtectedMACs: protected})
		if err != nil {
			return err
		}
		defer engine.Close()
		options.PolicySet = func(ctx context.Context, mac string, deny bool) (bool, bool, error) {
			status, err := engine.SetDenied(ctx, mac, deny)
			return status.Desired, status.Applied, err
		}
		options.PolicyStatus = func(mac string) (bool, bool) { status := engine.Status(mac); return status.Desired, status.Applied }
		options.PolicyReconcile = engine.Reconcile
	} else if !os.IsNotExist(policyErr) {
		return fmt.Errorf("policy journal unavailable")
	}
	service, err := app.New(ctx, options)
	if err != nil {
		return err
	}
	defer service.Close()
	var serving chan error
	if *socket != "" {
		serving = make(chan error, 1)
		go func() {
			err := rpc.Serve(ctx, *socket, rpc.Dispatch(commands))
			serving <- err
			if err != nil {
				cancel()
			}
		}()
	}
	runner := monitor.Runner{Backend: monitor.Ubus{Path: *ubus, Timeout: *timeout}, Registry: r, Log: log, ReconcileInterval: *interval, FlushInterval: *flush, CheckpointInterval: *checkpoint, ResubscribeInterval: *renew, RetryInterval: *retry, Bootstrap: *bootstrap, Save: save, BeforeAlert: service.PrepareAlert, OnNewDevice: service.Alert, Commands: commands, HandleCommand: service.Handle}
	runErr := runner.Run(ctx)
	cancel()
	if serving != nil {
		if err := <-serving; err != nil {
			return err
		}
	}
	return runErr
}

func protectedMACs(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	result := make([]string, 0)
	seen := make(map[string]bool)
	for _, value := range strings.Split(raw, ",") {
		mac, err := devices.NormalizeMAC(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("invalid protected MAC list")
		}
		if !seen[mac] {
			result = append(result, mac)
			seen[mac] = true
		}
	}
	return result, nil
}
