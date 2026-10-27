package storage

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/devices"
)

func TestReviewRestartRoundTrip(t *testing.T) {
	const mac = "02:00:00:00:00:01"
	scope := strings.Repeat("a", 64)
	at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	r := devices.New(100)
	r.Authorize(mac, "ap0", at)
	if err := r.EnsureReview(mac, scope, at); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(privateDir(t), "state.json")
	for _, action := range []string{"", "block_prompt", "block_confirm", "unblock_prompt", "unblock_confirm", "trust"} {
		if action != "" {
			if err := r.Decide(mac, r.State.Devices[mac].ReviewNonce, scope, action, at); err != nil {
				t.Fatal(action, err)
			}
		}
		before := r.State
		if err := Save(p, r.State); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(p, 100)
		if err != nil {
			t.Fatal(action, err)
		}
		if !reflect.DeepEqual(before, loaded.State) {
			t.Fatal("review or policy lost", action)
		}
		r = loaded
		if action == "block_prompt" || action == "unblock_prompt" {
			d := r.State.Devices[mac]
			if r.Decide(mac, d.ReviewNonce, scope, strings.TrimSuffix(action, "_prompt")+"_confirm", at.Add(3*time.Minute)) == nil {
				t.Fatal("expired persisted confirmation accepted")
			}
		}
	}
}

func TestReviewValidation(t *testing.T) {
	at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	valid := devices.Device{ReviewNonce: 1, ReviewScope: strings.Repeat("a", 64), ReviewStage: "review", ReviewIssued: at}
	for _, mutate := range []func(*devices.Device){
		func(d *devices.Device) { d.ReviewScope = strings.Repeat("A", 64) },
		func(d *devices.Device) { d.ReviewScope = strings.Repeat("g", 64) },
		func(d *devices.Device) { d.ReviewScope = strings.Repeat("a", 63) },
		func(d *devices.Device) { d.ReviewScope = "" },
		func(d *devices.Device) { d.ReviewNonce = 0 },
		func(d *devices.Device) { d.ReviewIssued = time.Time{} },
		func(d *devices.Device) { d.ReviewStage = "" },
		func(d *devices.Device) { d.ReviewStage = "invalid" },
		func(d *devices.Device) { d.ReviewStage = "decided" },
	} {
		r := devices.New(10)
		r.Authorize("02:00:00:00:00:01", "ap0", at)
		d := r.State.Devices["02:00:00:00:00:01"]
		d.ReviewNonce, d.ReviewScope, d.ReviewStage, d.ReviewIssued = valid.ReviewNonce, valid.ReviewScope, valid.ReviewStage, valid.ReviewIssued
		mutate(d)
		p := filepath.Join(privateDir(t), "state.json")
		if err := Save(p, r.State); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, 10); err == nil {
			t.Fatal("invalid review loaded", *d)
		}
	}
	for _, stage := range []string{"review", "block_confirm", "blocked", "unblock_confirm", "decided"} {
		d := valid
		d.ReviewStage = stage
		if stage == "decided" {
			d.ReviewNonce = 0
		}
		if err := validateReview(&d); err != nil {
			t.Fatal(stage, err)
		}
	}
	if err := validateReview(&devices.Device{}); err != nil {
		t.Fatal("legacy state rejected", err)
	}
}
