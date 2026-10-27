package app

import (
	"strings"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/telegram"
)

func TestAlertAndDecisionUseCurrentFriendlyName(t *testing.T) {
	f := newDecisionFixture(t, nil, nil, func(r *devices.Registry) { r.State.Devices[decisionMAC].FriendlyName = "Kitchen sensor" }, false)
	sender := f.s.sender.(*fakeSender)
	f.s.Alert(devices.Event{MAC: decisionMAC, At: time.Now(), Interface: "ap0"})
	select {
	case m := <-sender.messages:
		if m.Name != "Kitchen sensor" || m.MAC != decisionMAC {
			t.Fatalf("alert metadata: %+v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("missing alert")
	}
	result, err := f.action(t, telegram.ActionTrust)
	if err != nil || result.Name != "Kitchen sensor" || !strings.Contains(result.Text, "доверенным") {
		t.Fatalf("decision: %+v %v", result, err)
	}
	if d := f.device(t); d.Trust != "trusted" || d.DesiredDeny {
		t.Fatalf("unexpected policy change: %+v", d)
	}
}

func TestDecisionStatusExplainsBlockAndPending(t *testing.T) {
	f := newDecisionFixture(t, nil, nil, func(r *devices.Registry) { r.State.Devices[decisionMAC].FriendlyName = "Kitchen sensor" }, false)
	result, err := f.action(t, telegram.ActionBlockPrompt)
	if err != nil || result.Name != "Kitchen sensor" || !strings.Contains(result.Text, "не включена") {
		t.Fatalf("disabled status: %+v %v", result, err)
	}
	if f.device(t).DesiredDeny {
		t.Fatal("disabled block changed intent")
	}
}
