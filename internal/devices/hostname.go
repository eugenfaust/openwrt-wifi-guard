package devices

// DisplayName keeps operator-provided names independent of untrusted DHCP hints.
// An empty result lets presentation layers choose their localized unnamed label.
func (d Device) DisplayName() string {
	if d.FriendlyName != "" {
		return d.FriendlyName
	}
	return d.Hostname
}
