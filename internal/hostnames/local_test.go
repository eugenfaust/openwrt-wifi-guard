package hostnames

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLocalSources(t *testing.T) {
	now := time.Unix(1000, 0)
	uci := "package dhcp\nconfig host 'first'\n option name 'static-phone'\n option mac '02:00:00:00:00:01 02:00:00:00:00:02'\nconfig host\n option name 'later'\n list mac '02:00:00:00:00:01'\nconfig host\n option name 'bad<script>'\n option mac '02:00:00:00:00:03'\n"
	leases := "2000 02:00:00:00:00:01 192.0.2.1 dynamic *\n2000 02:00:00:00:00:03 192.0.2.3 lease-phone *\n999 02:00:00:00:00:04 192.0.2.4 expired *\n0 02:00:00:00:00:05 192.0.2.5 permanent *\n2000 ff:ff:ff:ff:ff:ff 192.0.2.6 invalid *\n2000 02:00:00:00:00:06 192.0.2.6 * *\n"
	got, err := parseSources([]byte(uci), []byte(leases), now)
	if err != nil {
		t.Fatal(err)
	}
	for mac, want := range map[string]string{"02:00:00:00:00:01": "static-phone", "02:00:00:00:00:02": "static-phone", "02:00:00:00:00:03": "lease-phone", "02:00:00:00:00:05": "permanent"} {
		if got[mac].Name != want {
			t.Fatalf("%s: %+v want %s", mac, got[mac], want)
		}
	}
	if len(got) != 4 || got["02:00:00:00:00:03"].Expires != time.Unix(2000, 0) {
		t.Fatalf("unexpected hints: %+v", got)
	}
}

func TestValidation(t *testing.T) {
	for _, name := range []string{"*", "", "-phone", "phone-", "a..b", "a_b", "a\nb", "<script>", strings.Repeat("a", 64), strings.Repeat("a.", 127) + "a"} {
		if ValidName(name) {
			t.Fatalf("accepted %q", name)
		}
	}
	for _, name := range []string{"phone", "Phone-1", "phone.lan", "phone.lan."} {
		if !ValidName(name) {
			t.Fatalf("rejected %q", name)
		}
	}
}

func TestLocalProviderReadOnlyInvocationAndMissingLeases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "uci")
	script := "#!/bin/sh\n[ \"$#\" = 5 ] && [ \"$1\" = '-q' ] && [ \"$2\" = '-c' ] && [ \"$3\" = 'synthetic-config' ] && [ \"$4\" = 'export' ] && [ \"$5\" = 'dhcp' ] || exit 9\ncat <<'EXPORT'\npackage dhcp\nconfig host\n option name 'static-phone.lan'\n list mac '02:00:00:00:00:01'\nconfig dnsmasq\n option ignored 'quote'\\''value'\nEXPORT\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := Local{UCI: path, ConfigDir: "synthetic-config", LeaseFile: filepath.Join(dir, "absent")}
	hints, err := p.Lookup(context.Background())
	if err != nil || len(hints) != 1 || hints["02:00:00:00:00:01"].Name != "static-phone.lan" {
		t.Fatalf("static-only provider: %v %v", hints, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Lookup(ctx); err == nil {
		t.Fatal("canceled provider succeeded")
	}
	for _, raw := range []string{"package wireless\n", "config host\noption name 'unterminated\n", "unexpected value\n"} {
		if _, err := parseSources([]byte(raw), nil, time.Now()); err == nil {
			t.Fatal("malformed export accepted")
		}
	}
}

func TestReadSafetyAndCommandBounds(t *testing.T) {
	dir := t.TempDir()
	leases := filepath.Join(dir, "leases")
	if err := syscall.Mkfifo(leases, 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := readLeases(leases); err == nil {
		t.Fatal("accepted FIFO")
	}
	if time.Since(start) > time.Second {
		t.Fatal("FIFO blocked")
	}
	if err := os.Remove(leases); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leases, []byte(strings.Repeat("x", maxInput+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readLeases(leases); err == nil {
		t.Fatal("accepted oversized file")
	}
	if err := os.WriteFile(leases, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "lease-link")
	if err := os.Symlink(leases, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readLeases(link); err == nil {
		t.Fatal("accepted symlink")
	}
	for _, script := range []string{"#!/bin/sh\nsleep 5\n", "#!/bin/sh\nyes x\n", "#!/bin/sh\nyes x >&2\n", "#!/bin/sh\nexit 1\n"} {
		path := filepath.Join(dir, "uci")
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		p := Local{UCI: path, LeaseFile: leases, Timeout: 30 * time.Millisecond}
		start = time.Now()
		if _, err := p.Lookup(context.Background()); err == nil {
			t.Fatal("accepted failed command")
		}
		if time.Since(start) > time.Second {
			t.Fatal("command not bounded")
		}
	}
}
