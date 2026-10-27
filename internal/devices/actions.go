package devices

import (
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
)

func (r *Registry) Rename(raw, name string, at time.Time) error {
	mac, err := NormalizeMAC(raw)
	if err != nil {
		return fmt.Errorf("invalid device")
	}
	d := r.State.Devices[mac]
	if d == nil {
		return fmt.Errorf("device not found")
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 80 {
		return fmt.Errorf("name must be at most 80 characters")
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return fmt.Errorf("name contains control characters")
		}
	}
	if d.FriendlyName != name {
		d.FriendlyName = name
		r.record("renamed", mac, d.LastInterface, at)
	}
	return nil
}
func (r *Registry) SetTrust(raw, trust string, at time.Time) error {
	mac, err := NormalizeMAC(raw)
	if err != nil {
		return fmt.Errorf("invalid device")
	}
	d := r.State.Devices[mac]
	if d == nil {
		return fmt.Errorf("device not found")
	}
	if trust != "unknown" && trust != "trusted" {
		return fmt.Errorf("trust must be unknown or trusted; blocking is not implemented")
	}
	if d.Trust != trust {
		d.Trust = trust
		r.record("trust_changed", mac, d.LastInterface, at)
	}
	return nil
}
