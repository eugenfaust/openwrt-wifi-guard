package main

import "testing"

func TestProtectedMACs(t *testing.T) {
	for _, raw := range []string{"", "  "} {
		got, err := protectedMACs(raw)
		if err != nil || len(got) != 0 {
			t.Fatal("empty list rejected")
		}
	}
	got, err := protectedMACs("02:00:00:00:00:01, 02:00:00:00:00:01,02:00:00:00:00:02")
	if err != nil || len(got) != 2 || got[0] != "02:00:00:00:00:01" || got[1] != "02:00:00:00:00:02" {
		t.Fatal("normalization/deduplication failed")
	}
	for _, raw := range []string{"not-a-mac", "02:00:00:00:00:01,", "ff:ff:ff:ff:ff:ff", "00:00:00:00:00:00", "02:00:00:00:00:01;reboot"} {
		if _, err := protectedMACs(raw); err == nil {
			t.Fatalf("invalid protected list accepted: %q", raw)
		}
	}
}
