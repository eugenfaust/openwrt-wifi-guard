package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/storage"
)

// No goroutines: tests exercise owner-only validation and inspect exactly which
// durable intent would reach the serialized worker.
func publicBlockOwner() *Service {
	r := devices.New(30)
	r.Authorize(decisionMAC, "ap0", time.Now())
	r.CompleteBootstrap()
	return &Service{registry: r, save: func(devices.State) error { return nil }, allowBlocking: true,
		policySet:   func(_ context.Context, _ string, deny bool) (bool, bool, error) { return deny, true, nil },
		policyQueue: make(chan policyJob, 16), protected: map[string]bool{}, policyActive: map[string]uint64{}}
}
func publicRequest(s *Service, ctx context.Context, method, params string) chan rpc.Result {
	reply := make(chan rpc.Result, 1)
	s.Handle(rpc.Command{Context: ctx, Method: method, Params: json.RawMessage(params), Reply: reply})
	return reply
}

const confirmedBlock = `{"mac":"02:00:00:00:00:01","revision":0,"confirmed":true}`

func TestPublicBlockRejectsBeforeIO(t *testing.T) {
	tests := []struct {
		name, params string
		setup        func(*Service)
	}{
		{"missing confirmation", `{"mac":"02:00:00:00:00:01","revision":0}`, nil},
		{"false confirmation", `{"mac":"02:00:00:00:00:01","revision":0,"confirmed":false}`, nil},
		{"null confirmation", `{"mac":"02:00:00:00:00:01","revision":0,"confirmed":null}`, nil},
		{"string confirmation", `{"mac":"02:00:00:00:00:01","revision":0,"confirmed":"true"}`, nil},
		{"missing revision", `{"mac":"02:00:00:00:00:01","confirmed":true}`, nil},
		{"null revision", `{"mac":"02:00:00:00:00:01","revision":null,"confirmed":true}`, nil},
		{"negative revision", `{"mac":"02:00:00:00:00:01","revision":-1,"confirmed":true}`, nil},
		{"stale revision", `{"mac":"02:00:00:00:00:01","revision":1,"confirmed":true}`, nil},
		{"unknown field", `{"mac":"02:00:00:00:00:01","revision":0,"confirmed":true,"deny":true}`, nil},
		{"invalid MAC", `{"mac":"bad","revision":0,"confirmed":true}`, nil},
		{"unknown MAC", `{"mac":"02:00:00:00:00:02","revision":0,"confirmed":true}`, nil},
		{"protected", confirmedBlock, func(s *Service) { s.protected[decisionMAC] = true }},
		{"disabled", confirmedBlock, func(s *Service) { s.allowBlocking = false }},
		{"no backend", confirmedBlock, func(s *Service) { s.policySet = nil }},
		{"active", confirmedBlock, func(s *Service) { s.policyActive[decisionMAC] = 1 }},
		{"pending", confirmedBlock, func(s *Service) { s.registry.State.Devices[decisionMAC].PolicyPending = true }},
		{"denied", confirmedBlock, func(s *Service) { s.registry.State.Devices[decisionMAC].DesiredDeny = true }},
		{"uncertain", confirmedBlock, func(s *Service) { s.decisionsUncertain = true }},
		{"full queue", confirmedBlock, func(s *Service) {
			for len(s.policyQueue) < cap(s.policyQueue) {
				s.policyQueue <- policyJob{}
			}
		}},
		{"overflow", `{"mac":"02:00:00:00:00:01","revision":18446744073709551615,"confirmed":true}`, func(s *Service) { s.registry.State.Devices[decisionMAC].PolicyRevision = ^uint64(0) }},
		{"backend pending", `{"mac":"02:00:00:00:00:01","revision":2,"confirmed":true}`, func(s *Service) {
			s.registry.State.Devices[decisionMAC].PolicyRevision = 2
			s.policyStatus = func(string) (bool, bool) { return false, false }
		}},
		{"backend drift", `{"mac":"02:00:00:00:00:01","revision":2,"confirmed":true}`, func(s *Service) {
			s.registry.State.Devices[decisionMAC].PolicyRevision = 2
			s.policyStatus = func(string) (bool, bool) { return true, true }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := publicBlockOwner()
			if tt.setup != nil {
				tt.setup(s)
			}
			before := *s.registry.State.Devices[decisionMAC]
			queued := len(s.policyQueue)
			s.save = func(devices.State) error { t.Fatal("save before validation"); return nil }
			if s.policySet != nil {
				s.policySet = func(context.Context, string, bool) (bool, bool, error) {
					t.Fatal("unexpected policy I/O")
					return false, false, nil
				}
			}
			result := <-publicRequest(s, context.Background(), "block", tt.params)
			if result.Err == nil {
				t.Fatal("unsafe request accepted")
			}
			if *s.registry.State.Devices[decisionMAC] != before || len(s.policyQueue) != queued {
				t.Fatal("rejected request changed intent")
			}
		})
	}
}
func TestPublicBlockDurableIntentAndFailure(t *testing.T) {
	for _, mode := range []string{"success", "unpublished", "published"} {
		t.Run(mode, func(t *testing.T) {
			s := publicBlockOwner()
			d := s.registry.State.Devices[decisionMAC]
			d.ReviewNonce = 42
			d.ReviewScope = "old"
			d.ReviewStage = "review"
			d.ReviewIssued = time.Now()
			before := *d
			saves := 0
			s.save = func(state devices.State) error {
				saves++
				saved := state.Devices[decisionMAC]
				if !saved.DesiredDeny || saved.PolicyRevision != 1 || !saved.PolicyPending || saved.ReviewNonce != 0 || saved.ReviewScope != "" || saved.ReviewStage != "" || !saved.ReviewIssued.IsZero() {
					t.Fatal("incomplete persisted intent")
				}
				if len(s.policyQueue) != 0 {
					t.Fatal("queued before save")
				}
				if mode == "published" {
					return &storage.PublishedError{Err: errors.New("fsync")}
				}
				if mode == "unpublished" {
					return errors.New("write")
				}
				return nil
			}
			reply := publicRequest(s, context.Background(), "block", confirmedBlock)
			if saves != 1 {
				t.Fatal("missing durable save")
			}
			if mode == "success" {
				if len(reply) != 0 || len(s.policyQueue) != 1 {
					t.Fatal("acknowledged before enforcement")
				}
				job := <-s.policyQueue
				if job.MAC != decisionMAC || job.Revision != 1 || !job.Deny || job.Telegram {
					t.Fatal("wrong worker job")
				}
				if _, err := s.checkPolicy(json.RawMessage(`{"MAC":"02:00:00:00:00:01","Revision":1,"Deny":true}`)); err != nil {
					t.Fatal(err)
				}
				value, err := s.finishPolicy(json.RawMessage(`{"MAC":"02:00:00:00:00:01","Revision":1,"Deny":true,"Applied":false,"Failed":true}`))
				if err != nil || value.(map[string]bool)["ok"] || !d.PolicyPending || !d.DesiredDeny {
					t.Fatal("failure not retained pending")
				}
			} else {
				if (<-reply).Err == nil || len(s.policyQueue) != 0 {
					t.Fatal("failed save acknowledged or queued")
				}
				if mode == "unpublished" && *d != before {
					t.Fatal("unpublished intent not restored")
				}
				if mode == "published" {
					if !d.DesiredDeny || !s.decisionsUncertain {
						t.Fatal("published intent rolled back")
					}
					if _, err := s.checkPolicy(json.RawMessage(`{"MAC":"02:00:00:00:00:01","Revision":1,"Deny":true}`)); err == nil {
						t.Fatal("uncertain enforcement allowed")
					}
					s.save = func(devices.State) error { return nil }
					if _, err := s.checkPolicy(json.RawMessage(`{"MAC":"02:00:00:00:00:01","Revision":1,"Deny":true}`)); err != nil {
						t.Fatal("durable recovery failed", err)
					}
				}
			}
		})
	}
}
func TestPublicBlockEligibility(t *testing.T) {
	cases := []struct {
		name, reason string
		setup        func(*Service)
	}{
		{"allowed", "", func(*Service) {}},
		{"protected", "protected", func(s *Service) { s.protected[decisionMAC] = true }},
		{"disabled", "disabled", func(s *Service) { s.allowBlocking = false }},
		{"no backend", "disabled", func(s *Service) { s.policySet = nil }},
		{"denied", "denied", func(s *Service) { s.registry.State.Devices[decisionMAC].DesiredDeny = true }},
		{"pending", "pending", func(s *Service) { s.registry.State.Devices[decisionMAC].PolicyPending = true }},
		{"active", "pending", func(s *Service) { s.policyActive[decisionMAC] = 1 }},
		{"uncertain", "pending", func(s *Service) { s.decisionsUncertain = true }},
		{"overflow", "pending", func(s *Service) { s.registry.State.Devices[decisionMAC].PolicyRevision = ^uint64(0) }},
		{"backend pending", "pending", func(s *Service) {
			s.registry.State.Devices[decisionMAC].PolicyRevision = 1
			s.policyStatus = func(string) (bool, bool) { return false, false }
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := publicBlockOwner()
			tt.setup(s)
			value, err := s.handle(context.Background(), "devices", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(value)
			var response struct {
				Devices []struct {
					BlockAllowed bool   `json:"block_allowed"`
					BlockReason  string `json:"block_reason"`
				}
			}
			if err = json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			d := response.Devices[0]
			if d.BlockAllowed != (tt.reason == "") || d.BlockReason != tt.reason {
				t.Fatalf("wrong eligibility: %s", raw)
			}
		})
	}
}
func TestPublicUnblockRecoveryAndStaleQueuedBlock(t *testing.T) {
	s := publicBlockOwner()
	publicRequest(s, context.Background(), "block", confirmedBlock)
	old := <-s.policyQueue
	s.allowBlocking = false
	s.protected[decisionMAC] = true
	reply := publicRequest(s, context.Background(), "unblock", `{"mac":"02:00:00:00:00:01","revision":1}`)
	if len(reply) != 0 || len(s.policyQueue) != 1 {
		t.Fatal("recovery unblock rejected")
	}
	job := <-s.policyQueue
	if job.Deny || job.Revision != 2 {
		t.Fatal("wrong recovery intent")
	}
	// Construct the internal check shape; public job fields are not accepted here.
	raw, _ := json.Marshal(map[string]any{"MAC": old.MAC, "Revision": old.Revision, "Deny": old.Deny})
	if _, err := s.checkPolicy(raw); err == nil {
		t.Fatal("obsolete deny accepted by worker")
	}
}

// Exercise the real asynchronous worker with Telegram entirely unconfigured.
func TestPublicBlockWorkerIndependentOfTelegram(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "applied", true: "failed"}[fail], func(t *testing.T) {
			r := publicBlockOwner().registry
			ctx, cancel := context.WithCancel(context.Background())
			q := make(chan rpc.Command, 16)
			started, release := make(chan struct{}), make(chan struct{})
			var saved atomic.Uint64
			var ioCalls atomic.Int32
			s, err := New(ctx, Options{Registry: r, Store: &fakeStore{}, Commands: q, AllowBlocking: true,
				Save: func(state devices.State) error { saved.Store(state.Devices[decisionMAC].PolicyRevision); return nil },
				PolicySet: func(ctx context.Context, mac string, deny bool) (bool, bool, error) {
					if mac != decisionMAC || !deny || saved.Load() != 1 {
						t.Error("policy ran before durable target intent")
					}
					ioCalls.Add(1)
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return deny, false, ctx.Err()
					}
					if fail {
						return deny, false, errors.New("backend failure")
					}
					return deny, true, nil
				},
			})
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					select {
					case <-ctx.Done():
						return
					case c := <-q:
						s.Handle(c)
					}
				}
			}()
			defer func() { cancel(); <-done; s.Close() }()
			caller, stop := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { _, err := rpc.Dispatch(q)(caller, "block", json.RawMessage(confirmedBlock)); result <- err }()
			ready(t, started)
			// Owner remains responsive while the target-only worker is blocked.
			value, err := rpc.Dispatch(q)(context.Background(), "devices", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			device := value.(map[string]any)["devices"].([]deviceView)[0]
			if !device.DesiredDeny || !device.PolicyPending || device.BlockAllowed {
				t.Fatal("missing pending intent")
			}
			if !fail {
				stop()
				if err := <-result; !errors.Is(err, context.Canceled) {
					t.Fatal("caller cancellation not returned", err)
				}
			}
			close(release)
			if fail {
				// Failed policy application is a confirmed saved decision, not a false success.
				if err := <-result; err != nil {
					t.Fatal(err)
				}
			}
			stop()
			deadline := time.Now().Add(2 * time.Second)
			for {
				value, err = rpc.Dispatch(q)(context.Background(), "devices", json.RawMessage(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				device = value.(map[string]any)["devices"].([]deviceView)[0]
				if device.PolicyPending == fail {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker completion missing")
				}
				time.Sleep(time.Millisecond)
			}
			if !device.DesiredDeny || device.PolicyRevision != 1 || ioCalls.Load() != 1 {
				t.Fatal("accepted intent lost")
			}
		})
	}
}

func TestPublicCanceledBlockHasNoIO(t *testing.T) {
	s := publicBlockOwner()
	before := *s.registry.State.Devices[decisionMAC]
	s.save = func(devices.State) error { t.Fatal("canceled request saved"); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	publicRequest(s, ctx, "block", confirmedBlock)
	if !reflect.DeepEqual(before, *s.registry.State.Devices[decisionMAC]) || len(s.policyQueue) != 0 {
		t.Fatal("canceled request mutated")
	}
}
