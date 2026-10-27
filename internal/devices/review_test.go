package devices

import (
	"bytes"
	"crypto/rand"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

const reviewMAC = "02:00:00:00:00:01"

var reviewScope = strings.Repeat("a", 64)

func reviewRegistry(t *testing.T) (*Registry, *Device, time.Time) {
	t.Helper()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := New(100)
	r.Authorize(reviewMAC, "ap0", at)
	if err := r.EnsureReview(reviewMAC, reviewScope, at); err != nil {
		t.Fatal(err)
	}
	return r, r.State.Devices[reviewMAC], at
}

func decide(t *testing.T, r *Registry, action string, at time.Time) {
	t.Helper()
	d := r.State.Devices[reviewMAC]
	old := d.ReviewNonce
	r.Dirty = false
	if err := r.Decide(reviewMAC, old, d.ReviewScope, action, at); err != nil {
		t.Fatal(action, err)
	}
	if !r.Dirty {
		t.Fatal("successful decision was not marked dirty")
	}
	if action != "trust" && (d.ReviewNonce == 0 || d.ReviewNonce == old) {
		t.Fatal("nonce was not rotated")
	}
}

func rejectUnchanged(t *testing.T, r *Registry, nonce uint64, scope, action string, at time.Time) {
	t.Helper()
	d := *r.State.Devices[reviewMAC]
	history := append([]Event(nil), r.State.History...)
	r.Dirty, r.Fresh = false, false
	if err := r.Decide(reviewMAC, nonce, scope, action, at); err == nil {
		t.Fatal("accepted", action)
	}
	if d != *r.State.Devices[reviewMAC] || !reflect.DeepEqual(history, r.State.History) || r.Dirty || r.Fresh {
		t.Fatal("rejected decision mutated state")
	}
}

func TestReviewBlockUnblockRejectsStaleActions(t *testing.T) {
	r, d, at := reviewRegistry(t)
	trustNonce := d.ReviewNonce
	decide(t, r, "block_prompt", at)
	blockNonce := d.ReviewNonce
	decide(t, r, "block_confirm", at.Add(time.Minute))
	if !d.DesiredDeny || !d.PolicyPending || d.PolicyRevision != 1 || d.ReviewStage != "blocked" {
		t.Fatal(*d)
	}
	rejectUnchanged(t, r, trustNonce, reviewScope, "trust", at)
	if r.SetTrust(reviewMAC, "trusted", at) == nil {
		t.Fatal("LuCI trust bypassed deny")
	}
	decide(t, r, "unblock_prompt", at.Add(time.Minute))
	decide(t, r, "unblock_confirm", at.Add(2*time.Minute))
	if d.DesiredDeny || !d.PolicyPending || d.PolicyRevision != 2 || d.ReviewStage != "review" {
		t.Fatal(*d)
	}
	rejectUnchanged(t, r, blockNonce, reviewScope, "block_confirm", at.Add(2*time.Minute))
	rejectUnchanged(t, r, trustNonce, reviewScope, "trust", at.Add(2*time.Minute))
	decide(t, r, "trust", at.Add(2*time.Minute))
	if d.Trust != "trusted" || d.ReviewNonce != 0 || d.ReviewStage != "decided" {
		t.Fatal(*d)
	}
}

func TestEnsureReviewPreservesLiveAndRenewsExpiredOrChangedScope(t *testing.T) {
	r, d, at := reviewRegistry(t)
	original := *d
	if err := r.EnsureReview(reviewMAC, reviewScope, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if original != *d {
		t.Fatal("live review changed")
	}
	decide(t, r, "block_prompt", at)
	original = *d
	if err := r.EnsureReview(reviewMAC, reviewScope, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if original != *d {
		t.Fatal("live confirmation changed")
	}
	if err := r.EnsureReview(reviewMAC, reviewScope, at.Add(2*time.Minute+time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if d.ReviewNonce == original.ReviewNonce || d.ReviewStage != "review" {
		t.Fatal("expired confirmation was not renewed")
	}
	rejectUnchanged(t, r, original.ReviewNonce, reviewScope, "block_confirm", at.Add(3*time.Minute))
	old := d.ReviewNonce
	if err := r.EnsureReview(reviewMAC, strings.Repeat("b", 64), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if old == d.ReviewNonce || d.ReviewScope == reviewScope {
		t.Fatal("scope change did not rotate")
	}
}

func TestConfirmationTTLAndCancel(t *testing.T) {
	for _, stage := range []string{"block_confirm", "unblock_confirm"} {
		for _, age := range []time.Duration{-time.Nanosecond, 2*time.Minute + time.Nanosecond} {
			t.Run(stage+age.String(), func(t *testing.T) {
				r, d, at := reviewRegistry(t)
				decide(t, r, "block_prompt", at)
				if stage == "unblock_confirm" {
					decide(t, r, "block_confirm", at)
					decide(t, r, "unblock_prompt", at)
				}
				rejectUnchanged(t, r, d.ReviewNonce, reviewScope, stage, at.Add(age))
				revision, deny, pending := d.PolicyRevision, d.DesiredDeny, d.PolicyPending
				decide(t, r, "cancel", at.Add(3*time.Minute))
				if d.PolicyRevision != revision || d.DesiredDeny != deny || d.PolicyPending != pending {
					t.Fatal("cancel changed policy")
				}
				want := "review"
				if deny {
					want = "blocked"
				}
				if d.ReviewStage != want {
					t.Fatal(d.ReviewStage)
				}
			})
		}
	}
	r, d, at := reviewRegistry(t)
	decide(t, r, "block_prompt", at)
	decide(t, r, "block_confirm", at.Add(2*time.Minute))
	if !d.DesiredDeny {
		t.Fatal("TTL boundary rejected")
	}
}

func TestDecisionInvalidActionsAndOverflow(t *testing.T) {
	r, d, at := reviewRegistry(t)
	for _, action := range []string{"", "TRUST", "block_confirm", "unblock_prompt", "unblock_confirm", "cancel"} {
		rejectUnchanged(t, r, d.ReviewNonce, reviewScope, action, at)
	}
	rejectUnchanged(t, r, 0, reviewScope, "trust", at)
	rejectUnchanged(t, r, d.ReviewNonce, strings.Repeat("b", 64), "trust", at)
	decide(t, r, "block_prompt", at)
	rejectUnchanged(t, r, d.ReviewNonce, reviewScope, "trust", at)
	d.PolicyRevision = math.MaxUint64
	rejectUnchanged(t, r, d.ReviewNonce, reviewScope, "block_confirm", at)
	d.DesiredDeny, d.ReviewStage = true, "unblock_confirm"
	rejectUnchanged(t, r, d.ReviewNonce, reviewScope, "unblock_confirm", at)
}

func TestTrustGuardInvalidationAndRename(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		r, d, at := reviewRegistry(t)
		if legacy {
			d.Trust = "blocked"
		} else {
			d.DesiredDeny = true
		}
		for _, trust := range []string{"trusted", "unknown"} {
			before := *d
			if r.SetTrust(reviewMAC, trust, at) == nil || *d != before {
				t.Fatal("blocked trust mutation allowed")
			}
		}
	}
	r, d, at := reviewRegistry(t)
	before := *d
	if err := r.Rename(reviewMAC, "test", at); err != nil {
		t.Fatal(err)
	}
	if d.ReviewNonce != before.ReviewNonce {
		t.Fatal("rename invalidated review")
	}
	history := len(r.State.History)
	r.Dirty = false
	if err := r.SetTrust(reviewMAC, "unknown", at); err != nil {
		t.Fatal(err)
	}
	if d.ReviewNonce != 0 || d.ReviewStage != "" || !d.ReviewIssued.IsZero() || d.ReviewScope != "" || !r.Dirty || len(r.State.History) != history {
		t.Fatal("no-op trust did not invalidate only review")
	}
}

type failedRandomReader struct{}

func (failedRandomReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestRandomFailureIsAtomicAndZeroIsRetried(t *testing.T) {
	r, d, at := reviewRegistry(t)
	oldReader := rand.Reader
	defer func() { rand.Reader = oldReader }()
	rand.Reader = failedRandomReader{}
	rejectUnchanged(t, r, d.ReviewNonce, reviewScope, "block_prompt", at)
	before := *d
	if r.EnsureReview(reviewMAC, strings.Repeat("b", 64), at) == nil || *d != before || r.Dirty {
		t.Fatal("failed renewal mutated state")
	}
	// Reject both zero and the previous nonce before accepting fresh entropy.
	rand.Reader = bytes.NewReader(append(make([]byte, 8), []byte{0, 0, 0, 0, 0, 0, 0, 1}...))
	if n, err := freshReviewNonce(2); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestEnsureReviewEligibilityAndInvalidContext(t *testing.T) {
	r, d, at := reviewRegistry(t)
	for _, scope := range []string{"", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		before := *d
		if r.EnsureReview(reviewMAC, scope, at) == nil || before != *d {
			t.Fatal("invalid scope accepted")
		}
	}
	for _, trust := range []string{"trusted", "blocked"} {
		d.Trust = trust
		if r.EnsureReview(reviewMAC, reviewScope, at) == nil {
			t.Fatal("ineligible review accepted")
		}
	}
	d.Trust, d.DesiredDeny = "unknown", true
	if r.EnsureReview(reviewMAC, reviewScope, at) == nil {
		t.Fatal("denied device reviewed")
	}
	if r.EnsureReview("02:00:00:00:00:02", reviewScope, at) == nil {
		t.Fatal("missing device reviewed")
	}
}
