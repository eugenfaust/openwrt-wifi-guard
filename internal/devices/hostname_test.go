package devices

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDisplayNameHostnameFallback(t *testing.T) {
	d := Device{}
	if d.DisplayName() != "" {
		t.Fatal("unnamed device must have empty backend name")
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), `"hostname"`) {
		t.Fatal("empty hostname not omitted")
	}
	d.Hostname = "dhcp-phone"
	if d.DisplayName() != "dhcp-phone" {
		t.Fatal("hostname not used")
	}
	d.FriendlyName = "My phone"
	if d.DisplayName() != "My phone" {
		t.Fatal("manual name not preferred")
	}
}
