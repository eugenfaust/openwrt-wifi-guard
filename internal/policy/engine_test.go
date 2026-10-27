package policy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const target = "02:00:00:00:00:21"
const admin = "02:00:00:00:00:22"
const other = "02:00:00:00:00:23"

func privateTemp(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	return d
}
func testEngine(t *testing.T) (*Engine, *fakeBackend) {
	t.Helper()
	e, err := Open(Options{Path: filepath.Join(privateTemp(t), "policy.json"), ProtectedMACs: []string{admin}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	b := &fakeBackend{t: t, e: e, sections: []section{{name: "home", kind: "wifi-iface", values: map[string][]string{"mode": {"ap"}}}}, aclSet: map[string]bool{}, banSet: map[string]bool{}, h: hash([]byte("before"))}
	e.backend = b
	return e, b
}

type fakeBackend struct {
	t                                                   *testing.T
	e                                                   *Engine
	sections                                            []section
	aclSet, banSet                                      map[string]bool
	h                                                   string
	failPrepare, failPublish, afterPublish, failControl bool
	operations                                          []string
	prepareWait                                         <-chan struct{}
}

func (b *fakeBackend) currentHash(ctx context.Context) (string, error) { return b.h, ctx.Err() }
func (b *fakeBackend) disk() journal {
	b.t.Helper()
	data, err := os.ReadFile(b.e.opts.Path)
	if err != nil {
		b.t.Fatal(err)
	}
	var j journal
	if err = json.Unmarshal(data, &j); err != nil {
		b.t.Fatal(err)
	}
	return j
}
func (b *fakeBackend) prepare(ctx context.Context, j journal) (*prepared, error) {
	if b.prepareWait != nil {
		select {
		case <-b.prepareWait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if b.failPrepare {
		return nil, errors.New("unsupported AP")
	}
	disk := b.disk()
	for m, v := range j.Entries {
		if disk.Entries[m] == nil || disk.Entries[m].Desired != v.Desired {
			b.t.Fatal("intent was not durable before preparation")
		}
	}
	changes, owners, aps, err := merge(b.sections, j)
	if err != nil {
		return nil, err
	}
	for i := range aps {
		aps[i].ifname = "fake-ap"
		aps[i].runtimeACL = copySet(b.aclSet)
	}
	after := b.h
	if len(changes) > 0 {
		data, _ := json.Marshal(changes)
		after = hash([]byte(b.h + string(data)))
	}
	return &prepared{before: b.h, after: after, owners: owners, aps: aps, close: func() {}, publish: func(ctx context.Context) error {
		if b.disk().Pending == nil {
			b.t.Fatal("publication without durable intent")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if b.failPublish {
			return errors.New("publication failure")
		}
		for _, c := range changes {
			for i := range b.sections {
				s := &b.sections[i]
				if s.name != c.section {
					continue
				}
				switch c.op {
				case "add_list":
					s.values[c.key] = append(s.values[c.key], c.value)
				case "del_list":
					var keep []string
					for _, v := range s.values[c.key] {
						if v != c.value {
							keep = append(keep, v)
						}
					}
					s.values[c.key] = keep
				case "set":
					s.values[c.key] = []string{c.value}
				case "delete":
					delete(s.values, c.key)
				}
			}
		}
		b.h = after
		b.operations = append(b.operations, "publish")
		if b.afterPublish {
			return errors.New("crash after rename")
		}
		return nil
	}}, nil
}
func copySet(m map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range m {
		out[k] = v
	}
	return out
}
func (b *fakeBackend) acl(ctx context.Context, _ string) (map[string]bool, error) {
	return copySet(b.aclSet), ctx.Err()
}
func (b *fakeBackend) bans(ctx context.Context, _ string) (map[string]bool, error) {
	return copySet(b.banSet), ctx.Err()
}
func (b *fakeBackend) control(ctx context.Context, iface, cmd string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.failControl {
		return errors.New("control failure")
	}
	w := strings.Fields(cmd)
	m := strings.ToLower(w[2])
	j := b.disk()
	if !j.Entries[m].Runtime[iface].ACL {
		b.t.Fatal("runtime side effect without durable ownership")
	}
	switch w[1] {
	case "ADD_MAC":
		b.aclSet[m] = true
	case "DEL_MAC":
		delete(b.aclSet, m)
	default:
		b.t.Fatal(cmd)
	}
	b.operations = append(b.operations, cmd)
	return nil
}
func (b *fakeBackend) disconnect(ctx context.Context, iface, m string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.operations = append(b.operations, "disconnect "+m)
	return nil
}
func TestBlockUnblockOwnedAndIdempotent(t *testing.T) {
	e, b := testEngine(t)
	s, err := e.SetDenied(context.Background(), target, true)
	if err != nil || !s.Applied || !s.Desired {
		t.Fatalf("%+v %v", s, err)
	}
	if !b.aclSet[target] || b.banSet[target] || len(e.state.Owners) != 1 {
		t.Fatal("missing permanent/runtime policy")
	}
	n := len(b.operations)
	if err = e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.operations) != n {
		t.Fatal("idempotent reconcile disconnected target again")
	}
	s, err = e.SetDenied(context.Background(), target, false)
	if err != nil || !s.Applied || s.Desired {
		t.Fatalf("%+v %v", s, err)
	}
	if b.aclSet[target] || b.banSet[target] || len(e.state.Owners) != 0 || one(b.sections[0], "macfilter") != "" {
		t.Fatal("owned policy not removed")
	}
}
func TestExternalDenyAndBanNeverRemoved(t *testing.T) {
	e, b := testEngine(t)
	b.sections[0].values["macfilter"] = []string{"deny"}
	b.sections[0].values["maclist"] = []string{target, other}
	b.aclSet[target] = true
	b.aclSet[other] = true
	b.banSet[target] = true
	if _, err := e.SetDenied(context.Background(), target, true); err != nil {
		t.Fatal(err)
	}
	if len(e.state.Owners) != 0 {
		t.Fatal("claimed external UCI")
	}
	if _, err := e.SetDenied(context.Background(), target, false); err != nil {
		t.Fatal(err)
	}
	if !b.aclSet[target] || !b.banSet[target] || len(b.sections[0].values["maclist"]) != 2 {
		t.Fatal("external restriction removed")
	}
}
func TestLoadedUCIUnblockWithoutPriorRuntimeIntent(t *testing.T) {
	e, b := testEngine(t)
	e.state.Entries[target] = &entry{Desired: true, Runtime: map[string]*runtimeOwnership{}}
	e.state.Owners = []ownership{{"home", target, ""}}
	b.sections[0].values["macfilter"] = []string{"deny"}
	b.sections[0].values["maclist"] = []string{target}
	b.aclSet[target] = true
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SetDenied(context.Background(), target, false); err != nil {
		t.Fatal(err)
	}
	if b.aclSet[target] {
		t.Fatal("UCI-loaded deny remains")
	}
	for _, op := range b.operations {
		if strings.Contains(op, "RELOAD") {
			t.Fatal("reload used")
		}
	}
}
func TestPublicationRecovery(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			e, b := testEngine(t)
			b.failPublish = !after
			b.afterPublish = after
			if _, err := e.SetDenied(context.Background(), target, true); err == nil {
				t.Fatal("expected failure")
			}
			if e.state.Pending == nil || b.aclSet[target] {
				t.Fatal("missing pending or unexpected runtime write")
			}
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(e.opts)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reopened.backend = b
			b.e = reopened
			b.failPublish = false
			b.afterPublish = false
			if err = reopened.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reopened.Status(target).Applied || !b.aclSet[target] || len(reopened.state.Owners) != 1 {
				t.Fatal("replay failed")
			}
		})
	}
}
func TestPendingDriftFailsClosed(t *testing.T) {
	e, b := testEngine(t)
	b.failPublish = true
	e.SetDenied(context.Background(), target, true)
	b.h = hash([]byte("external"))
	if err := e.Reconcile(context.Background()); err == nil {
		t.Fatal("drift accepted")
	}
	if len(b.operations) > 0 {
		t.Fatal("side effects on drift")
	}
}
func TestUnsupportedBeforeSideEffects(t *testing.T) {
	for _, values := range []map[string][]string{
		{"mode": {"ap"}, "macfilter": {"allow"}},
		{"mode": {"ap"}, "macfilter": {"disable"}, "maclist": {other}},
		{"mode": {"ap"}, "maclist": {other}},
		{"mode": {"ap"}, "macfile": {"/external"}},
		{"mode": {"ap"}, "macfilter": {"deny"}, "maclist": {other + " VLAN_ID=2"}},
	} {
		e, b := testEngine(t)
		b.sections = append(b.sections, section{name: "unsafe", kind: "wifi-iface", values: values})
		s, err := e.SetDenied(context.Background(), target, true)
		if err == nil || s.Applied || !s.Desired || len(b.operations) > 0 {
			t.Fatalf("unsafe partial success: %+v %v", s, err)
		}
	}
}
func TestProtectedAndCancellation(t *testing.T) {
	e, b := testEngine(t)
	if _, err := e.SetDenied(context.Background(), admin, true); err == nil {
		t.Fatal("protected denied")
	}
	e.protected = map[string]bool{}
	if _, err := e.SetDenied(context.Background(), target, true); err == nil {
		t.Fatal("missing protections accepted")
	}
	e.protected[admin] = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.SetDenied(ctx, target, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(b.operations) > 0 {
		t.Fatal("canceled side effects")
	}
}
func TestStatusDoesNotWaitForWorker(t *testing.T) {
	e, b := testEngine(t)
	wait := make(chan struct{})
	b.prepareWait = wait
	done := make(chan struct{})
	go func() { defer close(done); e.SetDenied(context.Background(), target, true) }()
	defer func() { close(wait); <-done }()
	deadline := time.After(time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("intent never published")
		default:
		}
		if e.Status(target).Desired {
			break
		}
		time.Sleep(time.Millisecond)
	}
	read := make(chan struct{})
	go func() { e.Status(target); close(read) }()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("Status blocked")
	}
}
func TestSerializedAndConcurrentStatus(t *testing.T) {
	e, _ := testEngine(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				e.Status(target)
				if _, err := e.SetDenied(context.Background(), target, j%2 == 0); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}
func TestJournalAndLockSafety(t *testing.T) {
	e, _ := testEngine(t)
	if _, err := Open(e.opts); err == nil {
		t.Fatal("second writer accepted")
	}
	e.Close()
	if err := os.Chmod(filepath.Dir(e.opts.Path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(e.opts); err == nil {
		t.Fatal("public parent accepted")
	}
}
func TestPreserveForeignEntriesAndModeOwnershipAcrossTargets(t *testing.T) {
	e, b := testEngine(t)
	ctx := context.Background()
	for _, m := range []string{target, other} {
		if _, err := e.SetDenied(ctx, m, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.SetDenied(ctx, target, false); err != nil {
		t.Fatal(err)
	}
	if one(b.sections[0], "macfilter") != "deny" || !b.aclSet[other] {
		t.Fatal("removed other owned deny")
	}
	if _, err := e.SetDenied(ctx, other, false); err != nil {
		t.Fatal(err)
	}
	if one(b.sections[0], "macfilter") != "" {
		t.Fatal("original mode not restored")
	}
}
func TestInactiveSectionsUntouched(t *testing.T) {
	e, b := testEngine(t)
	for _, mode := range []string{"sta", "mesh"} {
		b.sections = append(b.sections, section{kind: "wifi-iface", values: map[string][]string{"mode": {mode}, "macfilter": {"allow"}, "maclist": {other}}})
	}
	b.sections = append(b.sections, section{kind: "wifi-iface", values: map[string][]string{"mode": {"ap"}, "disabled": {"1"}, "macfilter": {"allow"}}})
	if _, err := e.SetDenied(context.Background(), target, true); err != nil {
		t.Fatal(err)
	}
}
