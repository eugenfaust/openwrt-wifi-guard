package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"openwrt-wifi-guard/internal/devices"
)

type Runner struct {
	Backend                                         Backend
	Registry                                        *devices.Registry
	Log                                             *slog.Logger
	ReconcileInterval, FlushInterval, RetryInterval time.Duration
	CheckpointInterval                              time.Duration
	ResubscribeInterval                             time.Duration
	Bootstrap                                       string
	Save                                            func(devices.State) error
}

func (r *Runner) Run(ctx context.Context) error {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	if r.ReconcileInterval <= 0 || r.FlushInterval <= 0 || r.RetryInterval <= 0 {
		return fmt.Errorf("intervals must be positive")
	}
	if r.Bootstrap != "import" && r.Bootstrap != "notify" {
		return fmt.Errorf("invalid bootstrap policy")
	}
	if r.CheckpointInterval <= 0 {
		r.CheckpointInterval = 15 * time.Minute
	}
	if r.ResubscribeInterval <= 0 {
		r.ResubscribeInterval = 5 * time.Minute
	}
	if r.Bootstrap == "notify" {
		r.Registry.CompleteBootstrap()
	}
	ticker := time.NewTicker(r.ReconcileInterval)
	defer ticker.Stop()
	flush := time.NewTicker(r.FlushInterval)
	defer flush.Stop()
	retry := time.NewTicker(r.RetryInterval)
	defer retry.Stop()
	lastSaved := time.Now()
	save := func(force bool) error {
		if !r.Registry.Dirty && (!r.Registry.Fresh || (!force && time.Since(lastSaved) < r.CheckpointInterval)) {
			return nil
		}
		if err := r.Save(r.Registry.State); err != nil {
			return err
		}
		r.Registry.Dirty = false
		r.Registry.Fresh = false
		lastSaved = time.Now()
		return nil
	}
	alert := func(e devices.Event) { r.Log.Info("new_device", "mac", e.MAC, "interface", e.Interface, "at", e.At) }
	handle := func(e Notice) {
		now := time.Now().UTC()
		if e.Kind == "authorized" {
			if r.Registry.Authorize(e.MAC, e.Interface, now) {
				alert(devices.Event{At: now, MAC: e.MAC, Interface: e.Interface})
			}
		} else {
			r.Registry.Disconnect(e.MAC, e.Interface, now)
		}
	}
	var lastSubscription time.Time
	var objects []string
	var events <-chan Notice
	var done <-chan error
	var cancel context.CancelFunc
	// Child completion and event delivery are separate channels. Always drain the
	// accepted event queue before discarding a subscription, including shutdown.
	stop := func() {
		if cancel != nil {
			cancel()
			if events != nil {
				for e := range events {
					handle(e)
				}
			}
			for range done {
			}
			cancel = nil
		}
		events = nil
		done = nil
	}
	defer stop()
	refresh := func() {
		next, err := r.Backend.Discover(ctx)
		if err != nil {
			r.Log.Warn("discovery_failed", "error", err)
			return
		}
		// Object names can survive a hostapd restart while object IDs change.
		// Periodic renewal repairs a still-alive but stale CLI subscription.
		renew := len(next) > 0 && time.Since(lastSubscription) >= r.ResubscribeInterval
		if !slices.Equal(next, objects) || (events == nil && len(next) > 0) || renew {
			stop()
			objects = next
			if len(objects) > 0 {
				var sub context.Context
				sub, cancel = context.WithCancel(ctx)
				events, done = r.Backend.Subscribe(sub, objects)
				lastSubscription = time.Now()
				r.Log.Info("subscribing", "objects", strings.Join(objects, ","))
			}
		}
		if len(objects) == 0 {
			for mac, d := range r.Registry.State.Devices {
				r.Registry.Disconnect(mac, d.LastInterface, time.Now().UTC())
			}
			r.Log.Warn("no_access_points")
			return
		}
		initialWindow := !r.Registry.State.Initialized
		if initialWindow {
			r.Registry.State.BootstrapPending = map[string]bool{}
			for _, obj := range objects {
				r.Registry.State.BootstrapPending[strings.TrimPrefix(obj, "hostapd.")] = true
			}
			r.Registry.Dirty = true
		}
		snapshots := map[string][]string{}
		complete := true
		for _, obj := range objects {
			clients, err := r.Backend.Clients(ctx, obj)
			if err != nil {
				complete = false
				r.Log.Warn("reconcile_failed", "object", obj, "error", err)
				continue
			}
			snapshots[strings.TrimPrefix(obj, "hostapd.")] = clients
		}
		for _, e := range r.Registry.ReconcileWithImports(snapshots, r.Registry.State.BootstrapPending, time.Now().UTC()) {
			alert(e)
		}
		for iface := range snapshots {
			if r.Registry.State.BootstrapPending[iface] {
				delete(r.Registry.State.BootstrapPending, iface)
				r.Registry.Dirty = true
			}
		}
		if initialWindow {
			// A defined finite boundary: events already queued during the initial
			// snapshot attempt belong to silent import, even if absent in its result.
			queued := len(events)
			for n := 0; n < queued; n++ {
				e, ok := <-events
				if !ok {
					events = nil
					break
				}
				handle(e)
			}
			r.Registry.CompleteBootstrap()
			r.Log.Info("bootstrap_event_window_closed", "known_devices", len(r.Registry.State.Devices), "pending_aps", len(r.Registry.State.BootstrapPending))
		}
		// A failing AP retains only its initial-snapshot suppression. Live events
		// continue alerting after the startup window; failure cannot silence forever.
		if complete {
			active := map[string]bool{}
			for iface := range snapshots {
				active[iface] = true
			}
			for mac, d := range r.Registry.State.Devices {
				if !active[d.LastInterface] {
					r.Registry.Disconnect(mac, d.LastInterface, time.Now().UTC())
				}
			}
			for iface := range r.Registry.State.BootstrapPending {
				if !active[iface] {
					delete(r.Registry.State.BootstrapPending, iface)
					r.Registry.Dirty = true
				}
			}
		}
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			stop()
			if err := save(true); err != nil {
				return fmt.Errorf("shutdown state save: %w", err)
			}
			return nil
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			handle(e)
		case err, ok := <-done:
			if ok && err != nil {
				r.Log.Warn("subscription_failed", "error", err)
			}
			stop()
		case <-ticker.C:
			refresh()
		case <-retry.C:
			if events == nil || !r.Registry.State.Initialized || len(r.Registry.State.BootstrapPending) > 0 {
				refresh()
			}
		case <-flush.C:
			if err := save(false); err != nil {
				r.Log.Error("state_save_failed", "error", err)
			}
		}
	}
}
