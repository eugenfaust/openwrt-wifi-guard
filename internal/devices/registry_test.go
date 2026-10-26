package devices

import (
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	for _, bad := range []string{"", "xx:00:00:00:00:01", "01:00:00:00:00:01", "00:00:00:00:00:00", "02:00:00:00:00:01:02:03"} {
		if _, err := NormalizeMAC(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if got, err := NormalizeMAC("02:AA:BB:00:00:01"); err != nil || got != "02:aa:bb:00:00:01" {
		t.Fatal(got, err)
	}
}
func TestLifecycle(t *testing.T) {
	r := New(4)
	now := time.Now().UTC()
	mac := "02:00:00:00:00:01"
	if r.Authorize(mac, "ap0", now) {
		t.Fatal("bootstrap alerted")
	}
	r.CompleteBootstrap()
	if r.Authorize(mac, "ap1", now.Add(time.Second)) {
		t.Fatal("roaming alerted")
	}
	if !r.Authorize("02:00:00:00:00:02", "ap0", now) {
		t.Fatal("new client not alerted")
	}
	if r.Authorize("02:00:00:00:00:02", "ap0", now) {
		t.Fatal("duplicate alerted")
	}
	r.Disconnect(mac, "ap0", now) // stale disconnect on previous band
	if !r.State.Devices[mac].Online {
		t.Fatal("stale roaming disconnect took client offline")
	}
	r.Disconnect(mac, "ap1", now)
	if r.State.Devices[mac].Online {
		t.Fatal("disconnect ignored")
	}
	if r.Authorize(mac, "ap1", now) {
		t.Fatal("reconnect alerted")
	}
	if len(r.State.History) > 4 {
		t.Fatal("unbounded history")
	}
	if r.State.Devices[mac].Trust != "unknown" {
		t.Fatal("known incorrectly implies trusted")
	}
}
func TestReconcilePartialAndBootstrapNotify(t *testing.T) {
	r := New(10)
	now := time.Now().UTC()
	r.Authorize("02:00:00:00:00:01", "ap0", now)
	r.CompleteBootstrap()
	r.Reconcile(map[string][]string{"ap1": {}}, now)
	if !r.State.Devices["02:00:00:00:00:01"].Online {
		t.Fatal("failed AP marked offline")
	}
	r.Reconcile(map[string][]string{"ap0": {}}, now)
	if r.State.Devices["02:00:00:00:00:01"].Online {
		t.Fatal("missed disconnect not reconciled")
	}
	r = New(10)
	r.CompleteBootstrap()
	if !r.Authorize("02:00:00:00:00:01", "ap0", now) {
		t.Fatal("notify bootstrap option ineffective")
	}
}
