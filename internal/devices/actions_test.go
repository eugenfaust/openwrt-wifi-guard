package devices

import (
	"strings"
	"testing"
	"time"
)

func TestDeviceActions(t *testing.T) {
	r := New(10)
	now := time.Now()
	mac := "02:00:00:00:00:01"
	r.Authorize(mac, "ap0", now)
	if err := r.Rename(mac, "Phone <test>", now); err != nil {
		t.Fatal(err)
	}
	if r.State.Devices[mac].FriendlyName != "Phone <test>" {
		t.Fatal("rename lost")
	}
	for _, name := range []string{strings.Repeat("a", 81), "hello\nworld"} {
		if r.Rename(mac, name, now) == nil {
			t.Fatal("invalid name accepted")
		}
	}
	if r.SetTrust(mac, "blocked", now) == nil {
		t.Fatal("permanent block exposed")
	}
	if err := r.SetTrust(mac, "trusted", now); err != nil {
		t.Fatal(err)
	}
	if r.State.Devices[mac].Trust != "trusted" {
		t.Fatal("trust not saved")
	}
	if r.Rename("02:00:00:00:00:02", "missing", now) == nil {
		t.Fatal("unknown device mutation allowed")
	}
}
