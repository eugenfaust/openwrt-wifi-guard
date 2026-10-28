package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
)

func TestDevicesDerivesPendingFromRuntimeWithoutMutation(t *testing.T) {
	s, r, _, _ := setup(t, false)
	d := r.State.Devices[decisionMAC]
	d.PolicyRevision = 1
	d.DesiredDeny = true
	d.PolicyPending = false
	for _, status := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		s.policyStatus = func(string) (bool, bool) { return status[0], status[1] }
		v, e := request(s, "devices", `{}`)
		if e != nil {
			t.Fatal(e)
		}
		copy := v.(map[string]any)["devices"].([]deviceView)[0].Device
		if copy.PolicyPending != (status != [2]bool{true, true}) {
			t.Fatalf("incorrect derived status: %v %+v", status, copy)
		}
		if d.PolicyPending {
			t.Fatal("read mutated registry")
		}
	}
}

func TestFailedReapplyPersistsPendingFromPreviouslyApplied(t *testing.T) {
	f := newDecisionFixture(t, nil, func(_ context.Context, _ string, deny bool) (bool, bool, error) {
		return deny, false, errors.New("runtime unavailable")
	}, func(r *devices.Registry) { blockedRegistry(r); r.State.Devices[decisionMAC].PolicyPending = false }, false)
	deadline := time.Now().Add(time.Second)
	for !f.device(t).PolicyPending {
		if time.Now().After(deadline) {
			t.Fatal("failed reapply left a stale success")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestJournalRecoveryWithoutRegistryRevisionRunsOffOwner(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, e := New(ctx, Options{Registry: devices.New(10), Store: &fakeStore{}, Save: func(devices.State) error { return nil }, Commands: make(chan rpc.Command), PolicySet: func(context.Context, string, bool) (bool, bool, error) {
		t.Error("no registry intent to enforce")
		return false, false, nil
	}, PolicyReconcile: func(ctx context.Context) error {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return errors.New("recovery unconfirmed")
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	defer close(release)
	ready(t, started)
	if _, e := request(s, "devices", `{}`); e != nil {
		t.Fatal("recovery blocked owner", e)
	}
	if calls.Load() != 1 {
		t.Fatal("journal recovery skipped or duplicated")
	}
}
