package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These command fixtures are deliberately fake; the socket exchange is real.
// No test reaches a router, system UCI configuration, or a hostapd process.
func commandFixtures(t *testing.T, b *systemBackend) string {
	t.Helper()
	d := privateTemp(t)
	config := "package wireless\nconfig wifi-iface home\n option mode ap\n"
	wireless := filepath.Join(d, "wireless")
	if err := os.WriteFile(wireless, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	uci := filepath.Join(d, "uci")
	ubus := filepath.Join(d, "ubus")
	if err := os.WriteFile(uci, []byte("#!/bin/sh\n[ \"$5\" = export ] || exit 50\nexec /bin/cat \"$2/wireless\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1:$2:$3" in
 call:network.wireless:status) printf '%s\n' '{"radio0":{"up":true,"interfaces":[{"section":"home","ifname":"ap"}]}}' ;;
 call:hostapd.ap:list_bans) printf '%s\n' '{"clients":[]}' ;;
 -v:list:hostapd.ap) printf '%s\n' '"del_client" "ban_time" "deauth" "reason"' ;;
 *) exit 51 ;;
esac
`
	if err := os.WriteFile(ubus, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	b.opts.Path = filepath.Join(d, "policy.json")
	b.opts.ConfigDir = d
	b.opts.UCI = uci
	b.opts.UBus = ubus
	return config
}
func TestPreflightRejectsExternalAcceptBeforeAnyPublication(t *testing.T) {
	for _, accept := range []string{target + "\n", "FAIL\n"} {
		t.Run(strings.TrimSpace(accept), func(t *testing.T) {
			b, _ := controlPeer(t, func(cmd string) string {
				switch cmd {
				case "PING":
					return "PONG"
				case "DENY_ACL SHOW":
					return ""
				case "ACCEPT_ACL SHOW":
					return accept
				}
				return "FAIL"
			})
			before := commandFixtures(t, b)
			j := journal{Version: 1, Entries: map[string]*entry{target: {Desired: true, Runtime: map[string]*runtimeOwnership{}}}}
			if p, err := b.prepare(context.Background(), j); err == nil {
				p.close()
				t.Fatal("external accept ACL accepted")
			}
			got, err := os.ReadFile(filepath.Join(b.opts.ConfigDir, "wireless"))
			if err != nil || string(got) != before {
				t.Fatal("preflight altered wireless")
			}
			files, _ := os.ReadDir(b.opts.ConfigDir)
			for _, f := range files {
				if strings.HasPrefix(f.Name(), ".policy-") {
					t.Fatal("failed preflight leaked staged secrets")
				}
			}
		})
	}
}
func TestPrepareReadOnlyNoChangeAndPublishDriftCheck(t *testing.T) {
	b, _ := controlPeer(t, func(cmd string) string {
		if cmd == "PING" {
			return "PONG"
		}
		return ""
	})
	before := commandFixtures(t, b)
	j := journal{Version: 1, Entries: map[string]*entry{target: {Runtime: map[string]*runtimeOwnership{}}}}
	p, err := b.prepare(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if p.before != p.after || len(p.aps) != 1 || p.aps[0].ifname != "ap" {
		t.Fatal("invalid no-op plan")
	}
	path := filepath.Join(b.opts.ConfigDir, "wireless")
	external := before + "# external concurrent change\n"
	if err = os.WriteFile(path, []byte(external), 0600); err != nil {
		t.Fatal(err)
	}
	if err = p.publish(context.Background()); err == nil {
		t.Fatal("drift overwritten")
	}
	got, _ := os.ReadFile(path)
	if string(got) != external {
		t.Fatal("external config lost")
	}
}
func TestCommandOutputNeverLeaksSecrets(t *testing.T) {
	script := filepath.Join(privateTemp(t), "failure")
	secret := "SYNTHETIC_DO_NOT_EXPOSE"
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' "+secret+"\nprintf '%s' "+secret+" >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := run(context.Background(), script)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe error: %v", err)
	}
}
