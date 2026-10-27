package devices

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"
)

const confirmationTTL = 2 * time.Minute

func validReviewScope(scope string) bool {
	if len(scope) != 64 {
		return false
	}
	for _, c := range scope {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func freshReviewNonce(previous uint64) (uint64, error) {
	var b [8]byte
	for {
		if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
			return 0, fmt.Errorf("generate review nonce: %w", err)
		}
		n := binary.BigEndian.Uint64(b[:])
		if n != 0 && n != previous {
			return n, nil
		}
	}
}

func confirmationLive(issued, at time.Time) bool {
	return !issued.IsZero() && !at.Before(issued) && at.Sub(issued) <= confirmationTTL
}

// EnsureReview prepares a credential-policy-bound review without doing storage or
// network I/O. The owner must durably save the registry before publishing buttons.
func (r *Registry) EnsureReview(raw, scope string, at time.Time) error {
	mac, err := NormalizeMAC(raw)
	if err != nil {
		return fmt.Errorf("invalid device")
	}
	d := r.State.Devices[mac]
	if d == nil || d.Trust != "unknown" || d.DesiredDeny {
		return fmt.Errorf("device is not eligible for review")
	}
	if !validReviewScope(scope) || at.IsZero() {
		return fmt.Errorf("invalid review context")
	}
	live := d.ReviewStage == "review" && !d.ReviewIssued.IsZero() && !at.Before(d.ReviewIssued)
	if d.ReviewStage == "block_confirm" {
		live = confirmationLive(d.ReviewIssued, at)
	}
	if d.ReviewNonce != 0 && d.ReviewScope == scope && live {
		r.Dirty = true
		return nil
	}
	nonce, err := freshReviewNonce(d.ReviewNonce)
	if err != nil {
		return err
	}
	d.ReviewNonce, d.ReviewScope, d.ReviewStage, d.ReviewIssued = nonce, scope, "review", at
	r.Dirty = true
	return nil
}

// Decide applies a single authenticated action. All validation and randomness
// precede mutation so failures leave the device, history and dirty flags intact.
func (r *Registry) Decide(raw string, nonce uint64, scope, action string, at time.Time) error {
	mac, err := NormalizeMAC(raw)
	if err != nil {
		return fmt.Errorf("invalid device")
	}
	d := r.State.Devices[mac]
	if d == nil || nonce == 0 || d.ReviewNonce != nonce || !validReviewScope(scope) || d.ReviewScope != scope || at.IsZero() {
		return fmt.Errorf("stale or invalid review")
	}
	next := *d
	stage := d.ReviewStage
	switch action {
	case "trust":
		if stage != "review" || d.DesiredDeny || d.Trust == "blocked" {
			return fmt.Errorf("trust is not available")
		}
		next.Trust = "trusted"
		next.ReviewStage, next.ReviewNonce = "decided", 0
	case "block_prompt":
		if stage != "review" || d.DesiredDeny || d.Trust == "blocked" {
			return fmt.Errorf("block prompt is not available")
		}
		next.ReviewStage = "block_confirm"
	case "block_confirm":
		if stage != "block_confirm" || d.DesiredDeny || d.Trust == "blocked" || !confirmationLive(d.ReviewIssued, at) {
			return fmt.Errorf("block confirmation is stale or unavailable")
		}
		if d.PolicyRevision == math.MaxUint64 {
			return fmt.Errorf("policy revision exhausted")
		}
		next.DesiredDeny, next.PolicyPending = true, true
		next.PolicyRevision++
		next.ReviewStage = "blocked"
	case "unblock_prompt":
		if stage != "blocked" || !d.DesiredDeny {
			return fmt.Errorf("unblock prompt is not available")
		}
		next.ReviewStage = "unblock_confirm"
	case "unblock_confirm":
		if stage != "unblock_confirm" || !d.DesiredDeny || !confirmationLive(d.ReviewIssued, at) {
			return fmt.Errorf("unblock confirmation is stale or unavailable")
		}
		if d.PolicyRevision == math.MaxUint64 {
			return fmt.Errorf("policy revision exhausted")
		}
		next.DesiredDeny, next.PolicyPending = false, true
		next.PolicyRevision++
		next.ReviewStage = "review"
	case "cancel":
		switch {
		case stage == "block_confirm" && !d.DesiredDeny && d.Trust != "blocked":
			next.ReviewStage = "review"
		case stage == "unblock_confirm" && d.DesiredDeny:
			next.ReviewStage = "blocked"
		default:
			return fmt.Errorf("no confirmation to cancel")
		}
	default:
		return fmt.Errorf("unknown review action")
	}
	if action != "trust" {
		next.ReviewNonce, err = freshReviewNonce(d.ReviewNonce)
		if err != nil {
			return err
		}
		next.ReviewIssued = at
	}
	changedTrust := d.Trust != next.Trust
	*d = next
	if changedTrust {
		r.record("trust_changed", mac, d.LastInterface, at)
	}
	r.record(action, mac, d.LastInterface, at)
	return nil
}
