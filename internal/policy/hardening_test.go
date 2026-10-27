package policy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPreexistingRuntimeACLNotClaimedAcrossRecovery(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "post-rename-crash"}[crash], func(t *testing.T) {
			e, b := testEngine(t)
			b.aclSet[target] = true
			b.afterPublish = crash
			_, err := e.SetDenied(context.Background(), target, true)
			if crash {
				if err == nil {
					t.Fatal("expected crash")
				}
				e.Close()
				reopened, err := Open(e.opts)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				e = reopened
				b.e = e
				e.backend = b
				b.afterPublish = false
				if err = e.Reconcile(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !e.state.Entries[target].Runtime["fake-ap"].ExternalACL {
				t.Fatal("external provenance lost")
			}
			if err = e.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err = e.SetDenied(context.Background(), target, false); err != nil {
				t.Fatal(err)
			}
			if !b.aclSet[target] {
				t.Fatal("external runtime ACL removed")
			}
			if len(e.state.Owners) != 0 {
				t.Fatal("owned UCI remains")
			}
		})
	}
}

func TestZeroDesiredAndStableReconcileDoNotWrite(t *testing.T) {
	e, b := testEngine(t)
	b.failPrepare = true
	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, err := e.SetDenied(context.Background(), target, false); err != nil || !s.Applied {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := os.Stat(e.opts.Path); !os.IsNotExist(err) {
		t.Fatal("empty policy wrote journal")
	}
	b.failPrepare = false
	if _, err := e.SetDenied(context.Background(), target, true); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(e.opts.Path)
	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(e.opts.Path)
	if !os.SameFile(before, after) {
		t.Fatal("stable reconcile rewrote journal")
	}
	if _, err := e.SetDenied(context.Background(), target, false); err != nil {
		t.Fatal(err)
	}
	before, _ = os.Stat(e.opts.Path)
	b.failPrepare = true
	for i := 0; i < 3; i++ {
		if err := e.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	after, _ = os.Stat(e.opts.Path)
	if !os.SameFile(before, after) {
		t.Fatal("zero desired rewrote journal")
	}
}

func TestStartupRequiresExplicitReconcileAndHonorsProtections(t *testing.T) {
	e, b := testEngine(t)
	b.failPrepare = true
	if _, err := e.SetDenied(context.Background(), target, true); err == nil {
		t.Fatal("expected preflight failure")
	}
	e.Close()
	o := e.opts
	o.ProtectedMACs = []string{target}
	reopened, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if s := reopened.Status(target); !s.Desired || s.Applied {
		t.Fatalf("startup status %+v", s)
	}
	reopened.backend = b
	b.e = reopened
	b.failPrepare = false
	if err = reopened.Reconcile(context.Background()); err == nil {
		t.Fatal("saved protected target applied")
	}
	if len(b.operations) != 0 {
		t.Fatal("startup protection side effect")
	}
}

func TestMissingRuntimeOwnerFailsBeforePublication(t *testing.T) {
	e, b := testEngine(t)
	e.state.Entries[target] = &entry{Desired: false, Runtime: map[string]*runtimeOwnership{"retired-ap": {ACL: true}}}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(context.Background()); err == nil {
		t.Fatal("missing runtime owner accepted")
	}
	if e.Status(target).Applied || len(b.operations) != 0 {
		t.Fatal("missing owner reported success or caused writes")
	}
}

func TestJournalRejectsUnsafeFilesAndOversize(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "public", "oversize", "trailing", "duplicate-owner", "bad-hash"} {
		t.Run(kind, func(t *testing.T) {
			d := privateTemp(t)
			path := filepath.Join(d, "policy.json")
			j := journal{Version: 1, Entries: map[string]*entry{target: {Desired: true, Runtime: map[string]*runtimeOwnership{}}}}
			switch kind {
			case "duplicate-owner":
				j.Owners = []ownership{{"home", target, ""}, {"home", target, ""}}
			case "bad-hash":
				j.Pending = &pending{Before: strings.Repeat("x", 64), After: strings.Repeat("0", 64)}
			}
			data, _ := json.Marshal(j)
			if kind == "oversize" {
				data = append(data, []byte(strings.Repeat(" ", maxJournalSize))...)
			}
			if kind == "trailing" {
				data = append(data, []byte(" {}")...)
			}
			if kind == "fifo" {
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "symlink":
					os.Rename(path, path+".real")
					if err := os.Symlink(path+".real", path); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(path, path+".copy"); err != nil {
						t.Fatal(err)
					}
				case "public":
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if e, err := Open(Options{Path: path}); err == nil {
				e.Close()
				t.Fatal("unsafe journal accepted")
			}
		})
	}
}

func TestAtomicPublicationDriftAndStaleBackup(t *testing.T) {
	d := privateTemp(t)
	path := filepath.Join(d, "wireless")
	old := []byte("original")
	backup := []byte("stale unrelated config")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".bak", backup, 0600); err != nil {
		t.Fatal(err)
	}
	drift := errors.New("drift")
	if err := atomicFile(path, []byte("new"), 0600, -1, -1, func() error { return drift }); !errors.Is(err, drift) {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(old) {
		t.Fatal("failed check replaced config")
	}
	if err := atomicFile(path, []byte("new"), 0600, -1, -1, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "new" {
		t.Fatal("publication absent")
	}
	got, _ = os.ReadFile(path + ".bak")
	if string(got) != string(backup) {
		t.Fatal("stale backup touched")
	}
	files, _ := os.ReadDir(d)
	if len(files) != 2 {
		t.Fatal("temporary publication file leaked")
	}
}

// A real local Unixgram peer exercises connected Linux abstract return addresses.
func controlPeer(t *testing.T, reply func(string) string) (*systemBackend, <-chan string) {
	t.Helper()
	dir := "@wifi-guard-test-" + hash([]byte(t.Name() + time.Now().String()))[:20]
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: dir + "/ap", Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	requests := make(chan string, 32)
	go func() {
		defer close(done)
		buf := make([]byte, 65536)
		for {
			n, from, err := conn.ReadFromUnix(buf)
			if err != nil {
				return
			}
			if !strings.HasPrefix(from.Name, "@wifi-guard-policy-") {
				requests <- "INVALID_LOCAL_ADDRESS"
			}
			cmd := string(buf[:n])
			requests <- cmd
			if r := reply(cmd); r != "NO_RESPONSE" {
				conn.WriteToUnix([]byte(r), from)
			}
		}
	}()
	t.Cleanup(func() { conn.Close(); <-done })
	return &systemBackend{opts: Options{ControlDir: dir}}, requests
}
func TestAbstractUnixgramControlAndACLParsing(t *testing.T) {
	b, requests := controlPeer(t, func(cmd string) string {
		switch cmd {
		case "PING":
			return "PONG\n"
		case "DENY_ACL SHOW":
			return strings.ToUpper(target) + " VLAN_ID=0\n"
		case "ACCEPT_ACL SHOW":
			return "\n"
		}
		return "OK\n"
	})
	if err := b.control(context.Background(), "ap", "PING"); err != nil {
		t.Fatal(err)
	}
	acl, err := b.acl(context.Background(), "ap")
	if err != nil || !acl[target] {
		t.Fatalf("%v %v", acl, err)
	}
	accept, err := b.readACL(context.Background(), "ap", "ACCEPT_ACL SHOW")
	if err != nil || len(accept) != 0 {
		t.Fatalf("%v %v", accept, err)
	}
	for _, cmd := range []string{"DENY_ACL CLEAR", "RELOAD", "ACCEPT_ACL ADD_MAC " + target, "DENY_ACL ADD_MAC " + target + "\nRELOAD"} {
		if err := b.control(context.Background(), "ap", cmd); err == nil {
			t.Fatalf("unsafe command accepted: %s", cmd)
		}
	}
	for i := 0; i < 3; i++ {
		if got := <-requests; got == "INVALID_LOCAL_ADDRESS" {
			t.Fatal(got)
		}
	}
	if safeName(".") || safeName("..") {
		t.Fatal("path traversal accepted")
	}
}
func TestControlCancellationAndUnsupportedResponses(t *testing.T) {
	b, _ := controlPeer(t, func(string) string { return "NO_RESPONSE" })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := b.control(ctx, "ap", "PING"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
	for _, reply := range []string{"FAIL", "UNKNOWN COMMAND", target + " VLAN_ID=1", target + " unexpected"} {
		t.Run(reply, func(t *testing.T) {
			b, _ := controlPeer(t, func(string) string { return reply })
			if _, err := b.acl(context.Background(), "ap"); err == nil {
				t.Fatal("unsupported ACL accepted")
			}
		})
	}
}
func TestUCIExportParserPreservesQuotedValues(t *testing.T) {
	sections, err := parseExport([]byte("package wireless\nconfig wifi-iface 'home'\n option mode 'ap'\n option key 'can'\\''t leak # secrets'\n list maclist '" + target + "'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || one(sections[0], "key") != "can't leak # secrets" {
		t.Fatalf("%+v", sections)
	}
	for _, raw := range []string{"option key 'unterminated", "package other", "config wifi-iface home\noption mode ap\noption mode sta"} {
		if _, err := parseExport([]byte(raw)); err == nil {
			t.Fatal("bad export accepted")
		}
	}
}
