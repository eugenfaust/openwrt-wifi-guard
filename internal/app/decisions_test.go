package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/storage"
	"openwrt-wifi-guard/internal/telegram"
)

const decisionMAC = "02:00:00:00:00:01"

type decisionFixture struct {
	s        *Service
	settings config.Settings
	stop     context.CancelFunc
	done     chan struct{}
}

func newDecisionFixture(t *testing.T, save func(devices.State) error, policy func(context.Context, string, bool) (bool, bool, error), init func(*devices.Registry), allow bool) *decisionFixture {
	t.Helper()
	r := devices.New(30)
	r.Authorize(decisionMAC, "ap0", time.Now())
	r.CompleteBootstrap()
	settings := config.Settings{Enabled: true, ChatID: "12345", Token: "12345:" + strings.Repeat("x", 30)}
	if err := r.EnsureReview(decisionMAC, credentialScope(settings), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if init != nil {
		init(r)
	}
	if save == nil {
		save = func(devices.State) error { return nil }
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := make(chan rpc.Command, 32)
	s, err := New(ctx, Options{Registry: r, Store: &fakeStore{settings: settings}, Save: save, Commands: q, Actions: true, AllowBlocking: allow, PolicySet: policy, NewSender: func(config.Settings) (Sender, error) {
		return &fakeSender{messages: make(chan telegram.Message, 10)}, nil
	}})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	f := &decisionFixture{s: s, settings: settings, stop: cancel, done: make(chan struct{})}
	go func() {
		defer close(f.done)
		for {
			select {
			case <-ctx.Done():
				return
			case c := <-q:
				s.Handle(c)
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-f.done; s.Close() })
	return f
}
func (f *decisionFixture) call(ctx context.Context, method string, p any) (any, error) {
	b, _ := json.Marshal(p)
	return rpc.Dispatch(f.s.commands)(ctx, method, b)
}
func (f *decisionFixture) device(t *testing.T) devices.Device {
	t.Helper()
	v, e := f.call(context.Background(), "devices", struct{}{})
	if e != nil {
		t.Fatal(e)
	}
	return v.(map[string]any)["devices"].([]deviceView)[0].Device
}
func claims(d devices.Device, a telegram.Action) telegram.ActionClaims {
	return telegram.ActionClaims{MAC: d.MAC, Nonce: d.ReviewNonce, Issued: d.ReviewIssued, Action: a}
}
func (f *decisionFixture) action(t *testing.T, a telegram.Action) (telegram.ActionResult, error) {
	t.Helper()
	return f.s.actionHandler(f.settings)(context.Background(), claims(f.device(t), a))
}
func ready(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("worker timeout")
	}
}

func TestDecisionStaleRotationAndPrivateSpoof(t *testing.T) {
	f := newDecisionFixture(t, nil, nil, nil, false)
	old := claims(f.device(t), telegram.ActionTrust)
	for _, m := range []string{"_telegram_action", "_policy_check", "_policy_snapshot", "_policy_done"} {
		if _, e := f.call(context.Background(), m, struct{}{}); e == nil {
			t.Fatalf("external %s accepted", m)
		}
	}
	if _, e := f.call(context.Background(), "configure", map[string]any{"token": "12345:" + strings.Repeat("y", 30)}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.actionHandler(f.settings)(context.Background(), old); e != nil {
		t.Fatal(e)
	}
	if f.device(t).Trust != "unknown" {
		t.Fatal("rotated credential accepted")
	}
	// Changing trust through LuCI invalidates the old message even when no-op.
	if _, e := f.call(context.Background(), "trust", map[string]string{"mac": decisionMAC, "trust": "unknown"}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.actionHandler(f.settings)(context.Background(), old); e != nil {
		t.Fatal(e)
	}
	if f.device(t).Trust != "unknown" {
		t.Fatal("stale callback accepted")
	}
}
func TestDecisionSaveFailureAndPublishedRecovery(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(fmt.Sprint(published), func(t *testing.T) {
			var mode atomic.Int32
			mode.Store(1)
			f := newDecisionFixture(t, func(devices.State) error {
				if mode.Load() == 0 {
					return nil
				}
				err := errors.New("save failed")
				if published {
					return &storage.PublishedError{Err: err}
				}
				return err
			}, nil, nil, false)
			before := f.device(t)
			if _, e := f.action(t, telegram.ActionTrust); e == nil {
				t.Fatal("save failure acknowledged")
			}
			after := f.device(t)
			if published {
				if after.Trust != "trusted" {
					t.Fatal("published mutation rolled back")
				}
			} else if after != before {
				t.Fatal("unpublished mutation not restored")
			}
			// An uncertain publication must not be confirmed by a private snapshot.
			if published {
				if _, e := f.s.dispatchDecision(context.Background(), "_policy_snapshot", json.RawMessage(`{}`)); e == nil {
					t.Fatal("unconfirmed save accepted")
				}
			}
			mode.Store(0)
			if _, e := f.s.dispatchDecision(context.Background(), "_policy_snapshot", json.RawMessage(`{}`)); e != nil {
				t.Fatal(e)
			}
			if !published {
				if _, e := f.action(t, telegram.ActionTrust); e != nil {
					t.Fatal(e)
				}
			}
			if f.device(t).Trust != "trusted" {
				t.Fatal("retry failed")
			}
		})
	}
}
func blockedRegistry(r *devices.Registry) {
	d := r.State.Devices[decisionMAC]
	d.DesiredDeny = true
	d.PolicyRevision = 1
	d.PolicyPending = true
	d.ReviewStage = "blocked"
}
func TestPolicyWorkerNonblockingAndExplicitUnblock(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	f := newDecisionFixture(t, nil, func(ctx context.Context, _ string, deny bool) (bool, bool, error) {
		calls.Add(1)
		started <- struct{}{}
		if deny {
			select {
			case <-release:
			case <-ctx.Done():
				return deny, false, ctx.Err()
			}
		}
		return deny, true, nil
	}, blockedRegistry, false)
	ready(t, started)
	if _, e := f.call(context.Background(), "settings", struct{}{}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.call(context.Background(), "trust", map[string]string{"mac": decisionMAC, "trust": "trusted"}); e == nil {
		t.Fatal("trust removed deny")
	}
	if _, e := f.action(t, telegram.ActionTrust); e != nil {
		t.Fatal(e)
	}
	if !f.device(t).DesiredDeny {
		t.Fatal("telegram trust removed deny")
	}
	// In-flight reservation rejects superseding intent, instead of letting stale
	// policy I/O apply a deny after an accepted unblock.
	if _, e := f.call(context.Background(), "unblock", map[string]any{"mac": decisionMAC, "revision": 1}); e == nil {
		t.Fatal("in-flight revision superseded")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for f.device(t).PolicyPending {
		if time.Now().After(deadline) {
			t.Fatal("completion not routed")
		}
		time.Sleep(time.Millisecond)
	}
	if _, e := f.call(context.Background(), "unblock", map[string]any{"mac": decisionMAC, "revision": 0}); e == nil {
		t.Fatal("stale revision accepted")
	}
	if _, e := f.call(context.Background(), "unblock", map[string]any{"mac": decisionMAC, "revision": 1}); e != nil {
		t.Fatal(e)
	}
	d := f.device(t)
	if d.DesiredDeny || d.PolicyPending || d.PolicyRevision != 2 || calls.Load() != 2 {
		t.Fatalf("unblock incomplete: %+v calls=%d", d, calls.Load())
	}
}
func TestPolicyCanceledCallerDoesNotCancelAcceptedIntent(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	f := newDecisionFixture(t, nil, func(ctx context.Context, _ string, deny bool) (bool, bool, error) {
		close(started)
		select {
		case <-release:
			close(finished)
			return deny, true, nil
		case <-ctx.Done():
			return deny, false, ctx.Err()
		}
	}, nil, true)
	if _, e := f.action(t, telegram.ActionBlockPrompt); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reply := make(chan error, 1)
	go func() {
		_, e := f.s.actionHandler(f.settings)(ctx, claims(fDeviceForAction(f), telegram.ActionBlockConfirm))
		reply <- e
	}()
	ready(t, started)
	cancel()
	if e := <-reply; !errors.Is(e, context.Canceled) {
		t.Fatalf("caller cancellation: %v", e)
	}
	close(release)
	ready(t, finished)
	deadline := time.Now().Add(2 * time.Second)
	for {
		d := f.device(t)
		if d.DesiredDeny && !d.PolicyPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted intent lost")
		}
		time.Sleep(time.Millisecond)
	}
}

// Obtain callback claims through the owner, never by sharing the live registry.
func fDeviceForAction(f *decisionFixture) devices.Device {
	v, _ := f.call(context.Background(), "devices", struct{}{})
	return v.(map[string]any)["devices"].([]deviceView)[0].Device
}
func TestPolicyShutdownCancellation(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	f := newDecisionFixture(t, nil, func(ctx context.Context, _ string, deny bool) (bool, bool, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return deny, false, ctx.Err()
	}, blockedRegistry, false)
	ready(t, started)
	f.stop()
	ready(t, stopped)
}
func TestCanceledDecisionNeverMutates(t *testing.T) {
	f := newDecisionFixture(t, nil, nil, nil, false)
	before := f.device(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := f.s.actionHandler(f.settings)(ctx, claims(before, telegram.ActionTrust)); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if f.device(t) != before {
		t.Fatal("canceled action mutated registry")
	}
}
func TestBlockOptInAndProtected(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(fmt.Sprint(protected), func(t *testing.T) {
			f := newDecisionFixture(t, nil, nil, nil, protected)
			// No worker is started for these rejected actions.
			f.s.policySet = func(context.Context, string, bool) (bool, bool, error) {
				t.Fatal("unexpected policy I/O")
				return false, false, nil
			}
			if protected {
				f.s.protected[decisionMAC] = true
			}
			before := f.device(t)
			if _, e := f.action(t, telegram.ActionBlockPrompt); e != nil {
				t.Fatal(e)
			}
			if f.device(t) != before {
				t.Fatal("unsafe block enabled")
			}
		})
	}
}
