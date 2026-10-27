package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/telegram"
)

func TestTelegramBlockAndUnblockWorkflow(t *testing.T) {
	var calls atomic.Int32
	f := newDecisionFixture(t, nil, func(_ context.Context, _ string, deny bool) (bool, bool, error) { calls.Add(1); return deny, true, nil }, nil, true)
	old := claims(f.device(t), telegram.ActionBlockConfirm)
	if _, e := f.action(t, telegram.ActionBlockPrompt); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.actionHandler(f.settings)(context.Background(), old); e != nil {
		t.Fatal(e)
	}
	if f.device(t).DesiredDeny || calls.Load() != 0 {
		t.Fatal("stale confirmation enforced")
	}
	if _, e := f.action(t, telegram.ActionCancel); e != nil {
		t.Fatal(e)
	}
	if f.device(t).ReviewStage != "review" {
		t.Fatal("cancel not routed")
	}
	for _, a := range []telegram.Action{telegram.ActionBlockPrompt, telegram.ActionBlockConfirm, telegram.ActionUnblockPrompt, telegram.ActionUnblockConfirm} {
		if _, e := f.action(t, a); e != nil {
			t.Fatal(e)
		}
	}
	d := f.device(t)
	if d.DesiredDeny || d.PolicyPending || d.PolicyRevision != 2 || calls.Load() != 2 {
		t.Fatalf("workflow incomplete: %+v calls=%d", d, calls.Load())
	}
}

func TestPolicySaveFailurePreventsIO(t *testing.T) {
	var fail atomic.Bool
	var calls atomic.Int32
	f := newDecisionFixture(t, func(devices.State) error {
		if fail.Load() {
			return errors.New("disk full")
		}
		return nil
	}, func(_ context.Context, _ string, deny bool) (bool, bool, error) { calls.Add(1); return deny, true, nil }, nil, true)
	if _, e := f.action(t, telegram.ActionBlockPrompt); e != nil {
		t.Fatal(e)
	}
	before := f.device(t)
	fail.Store(true)
	if _, e := f.action(t, telegram.ActionBlockConfirm); e == nil {
		t.Fatal("unsaved deny acknowledged")
	}
	if f.device(t) != before || calls.Load() != 0 {
		t.Fatal("unsaved deny mutated or applied")
	}
}

func TestPolicyStaleQueuedJobAndFailureRemainPending(t *testing.T) {
	var calls atomic.Int32
	f := newDecisionFixture(t, nil, func(_ context.Context, _ string, deny bool) (bool, bool, error) {
		calls.Add(1)
		return deny, false, errors.New("backend unavailable")
	}, nil, true)
	if _, e := f.action(t, telegram.ActionBlockPrompt); e != nil {
		t.Fatal(e)
	}
	result, e := f.action(t, telegram.ActionBlockConfirm)
	if e != nil {
		t.Fatal(e)
	}
	if result.Text == "" || !f.device(t).PolicyPending || !f.device(t).DesiredDeny {
		t.Fatal("failed enforcement acknowledged as applied")
	}
	// A stale queued job is validated by the owner, not merely by the worker.
	reply := make(chan rpc.Result, 1)
	f.s.policyQueue <- policyJob{MAC: decisionMAC, Revision: 1, Deny: false, Reply: reply}
	select {
	case r := <-reply:
		if r.Err == nil {
			t.Fatal("obsolete job accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("obsolete job stalled")
	}
	if calls.Load() != 1 {
		t.Fatal("obsolete job reached backend")
	}
	// A successful completion must carry an active owner reservation.
	completion, _ := json.Marshal(policyCompletion{MAC: decisionMAC, Revision: 1, Deny: true, Applied: true})
	if _, e := f.s.dispatchDecision(context.Background(), "_policy_done", completion); e == nil {
		t.Fatal("unreserved completion accepted")
	}
	if !f.device(t).PolicyPending {
		t.Fatal("unreserved completion cleared pending")
	}
}
