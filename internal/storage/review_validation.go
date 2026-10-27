package storage

import (
	"fmt"

	"openwrt-wifi-guard/internal/devices"
)

func validateReview(d *devices.Device) error {
	if d.ReviewScope != "" {
		if len(d.ReviewScope) != 64 {
			return fmt.Errorf("invalid review scope")
		}
		for _, c := range d.ReviewScope {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return fmt.Errorf("invalid review scope")
			}
		}
	}
	switch d.ReviewStage {
	case "":
		if d.ReviewNonce != 0 || d.ReviewScope != "" || !d.ReviewIssued.IsZero() {
			return fmt.Errorf("incomplete review state")
		}
	case "review", "block_confirm", "blocked", "unblock_confirm", "decided":
		if d.ReviewScope == "" || d.ReviewIssued.IsZero() || (d.ReviewNonce == 0) != (d.ReviewStage == "decided") {
			return fmt.Errorf("incomplete review state")
		}
	default:
		return fmt.Errorf("invalid review stage")
	}
	// Expiry is checked by the decision domain, not against the load-time clock:
	// an expired confirmation must not prevent the daemon from restarting.
	return nil
}
