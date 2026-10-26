package storage

import (
	"openwrt-wifi-guard/internal/devices"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(privateDir(t), "state.json")
	r := devices.New(3)
	r.Authorize("02:00:00:00:00:01", "ap0", time.Now().UTC())
	r.CompleteBootstrap()
	r.State.BootstrapPending = map[string]bool{"ap1": true}
	if err := Save(p, r.State); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !s.State.Initialized || len(s.State.Devices) != 1 || !s.State.BootstrapPending["ap1"] {
		t.Fatal("state lost")
	}
	if s.Authorize("02:00:00:00:00:01", "ap1", time.Now()) {
		t.Fatal("restart duplicates")
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}
func TestCorruptStateFailsClosed(t *testing.T) {
	p := filepath.Join(privateDir(t), "state.json")
	for _, data := range []string{`{`, `{"version":99}`, `{"version":1,"devices":{"broken":{}}}`, `{"version":1,"devices":null}`} {
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p, 3); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}
