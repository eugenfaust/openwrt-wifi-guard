package monitor

import "testing"

func TestEvents(t *testing.T) {
	for _, line := range []string{`{"probe":{"address":"02:00:00:00:00:01"}}`, `{"auth":{"address":"02:00:00:00:00:01"}}`, `{"assoc":{"address":"02:00:00:00:00:01"}}`, `{`, `{"sta-authorized":{"address":"bad"}}`, `{"sta-authorized":{"address":"02:00:00:00:00:01"}}`} {
		if _, ok := ParseEvent([]byte(line)); ok {
			t.Fatalf("accepted %s", line)
		}
	}
	e, ok := ParseEvent([]byte(`{"sta-authorized":{"address":"02:AA:00:00:00:01","ifname":"phy1-ap0","auth-alg":"open"}}`))
	if !ok || e.MAC != "02:aa:00:00:00:01" || e.Kind != "authorized" {
		t.Fatal(e, ok)
	}
	e, ok = ParseEvent([]byte(`{"disassoc":{"address":"02:00:00:00:00:01","ifname":"phy1-ap0"}}`))
	if !ok || e.Kind != "disconnected" {
		t.Fatal(e, ok)
	}
}
func TestClients(t *testing.T) {
	c, err := ParseClients([]byte(`{"clients":{"02:00:00:00:00:01":{"authorized":true},"02:00:00:00:00:02":{"authorized":false},"02:00:00:00:00:03":{"auth":true}}}`))
	if err != nil || len(c) != 1 {
		t.Fatal(c, err)
	}
	for _, s := range []string{`{}`, `{"clients":null}`, `{"clients":{"02:00:00:00:00:01":{"authorized":1}}}`} {
		if _, err := ParseClients([]byte(s)); err == nil {
			t.Fatal("invalid snapshot accepted", s)
		}
	}
}
