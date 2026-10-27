package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExternalHostapdACLOverridesRejected(t *testing.T) {
	for _, opt := range []string{"macaddr_acl=1", "accept_mac_file=/external", "deny_mac_file=/external"} {
		e, b := testEngine(t)
		b.sections[0].values["hostapd_options"] = []string{opt}
		if _, err := e.SetDenied(context.Background(), target, true); err == nil {
			t.Fatal("external ACL override accepted")
		}
		if len(b.operations) != 0 {
			t.Fatal("unsupported override caused side effects")
		}
	}
}
func TestAbsentPersistentOwnerDoesNotClaimRuntime(t *testing.T) {
	e, b := testEngine(t)
	e.state.Entries[target] = &entry{Runtime: map[string]*runtimeOwnership{}}
	e.state.Owners = []ownership{{"home", target, "deny"}}
	b.sections[0].values["macfilter"] = []string{"deny"}
	b.aclSet[target] = true
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !b.aclSet[target] {
		t.Fatal("claimed runtime rule with missing persistent provenance")
	}
}
func TestLegacyBanWaitsInsteadOfClearingExternalBan(t *testing.T) {
	e, b := testEngine(t)
	e.state.Entries[target] = &entry{Runtime: map[string]*runtimeOwnership{"fake-ap": {Ban: true, BanUntil: time.Now().Add(time.Minute).UnixMilli()}}}
	b.banSet[target] = true
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(context.Background()); err == nil {
		t.Fatal("active legacy ban did not require waiting")
	}
	if !b.banSet[target] || len(b.operations) != 0 {
		t.Fatal("legacy recovery touched external ban")
	}
	e.state.Entries[target].Runtime["fake-ap"].BanUntil = time.Now().Add(-time.Minute).UnixMilli()
	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !b.banSet[target] {
		t.Fatal("expired ownership cleared externally replaced ban")
	}
}
func TestJournalOversizedSavePreservesLastDurableState(t *testing.T) {
	e, _ := testEngine(t)
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(e.opts.Path)
	if err != nil {
		t.Fatal(err)
	}
	e.state.Entries[strings.Repeat("x", maxJournalSize)] = &entry{Runtime: map[string]*runtimeOwnership{}}
	if err := e.save(); err == nil {
		t.Fatal("oversized journal saved")
	}
	after, err := os.ReadFile(e.opts.Path)
	if err != nil || string(before) != string(after) {
		t.Fatal("oversized save lost durable state")
	}
	if err := e.Reconcile(context.Background()); err == nil {
		t.Fatal("uncertain durability did not stop mutations")
	}
}
func TestDisconnectNeverSetsOrClearsBanTime(t *testing.T) {
	b := &systemBackend{}
	d := privateTemp(t)
	b.opts.UBus = filepath.Join(d, "ubus")
	script := "#!/bin/sh\n[ \"$1:$2:$3\" = 'call:hostapd.ap:del_client' ] || exit 1\nprintf '%s' \"$4\" > \"$0.request\"\n"
	if err := os.WriteFile(b.opts.UBus, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := b.disconnect(context.Background(), "ap", target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(b.opts.UBus + ".request")
	if err != nil {
		t.Fatal(err)
	}
	payload := string(data)
	if strings.Contains(payload, "ban_time") || !strings.Contains(payload, `"addr":"`+target+`"`) || !strings.Contains(payload, `"deauth":true`) {
		t.Fatalf("incorrect disconnect payload: %s", payload)
	}
}
