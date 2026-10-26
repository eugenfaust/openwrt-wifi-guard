package storage

import (
	"openwrt-wifi-guard/internal/devices"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOversizedSavePreservesWorkingState(t *testing.T) {
	p := filepath.Join(privateDir(t), "state.json")
	r := devices.New(5)
	r.Authorize("02:00:00:00:00:01", "ap0", time.Now())
	if err := Save(p, r.State); err != nil {
		t.Fatal(err)
	}
	r.State.Devices["02:00:00:00:00:01"].FriendlyName = strings.Repeat("x", (16<<20)+1)
	if err := Save(p, r.State); err == nil {
		t.Fatal("oversized state saved")
	}
	restored, err := Load(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State.Devices["02:00:00:00:00:01"].FriendlyName != "" {
		t.Fatal("previous state replaced")
	}
}
func TestRejectWritableDirectory(t *testing.T) {
	dir := privateDir(t)
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "state.json")
	if err := Save(p, devices.New(5).State); err == nil {
		t.Fatal("unsafe state directory allowed")
	}
	if _, err := Load(p, 5); err == nil {
		t.Fatal("unsafe read directory allowed")
	}
}
func TestRejectSymlinkState(t *testing.T) {
	dir := privateDir(t)
	p := filepath.Join(dir, "state.json")
	target := filepath.Join(dir, "other.json")
	if err := Save(target, devices.New(5).State); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, 5); err == nil {
		t.Fatal("symlink state accepted")
	}
}
