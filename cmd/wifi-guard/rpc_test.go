package main

import (
	"bytes"
	"encoding/json"
	"openwrt-wifi-guard/internal/rpc"
	"testing"
)

func TestRPCDTransportMetadata(t *testing.T) {
	raw, err := pluginParams([]byte(`{"ubus_rpc_session":"0123456789abcdef0123456789abcdef","name":"Test","mac":"02:00:00:00:00:01"}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("ubus_rpc_session")) {
		t.Fatal("transport session forwarded as application input")
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil || fields["name"] != "Test" {
		t.Fatal("application params changed")
	}
	for _, bad := range []string{`null`, `[]`, `broken`} {
		if _, err := pluginParams([]byte(bad)); err == nil {
			t.Fatal("non-object accepted")
		}
	}
}
func TestRPCDValueTypedSchema(t *testing.T) {
	schema := rpc.Methods()
	encoded, _ := json.Marshal(schema)
	var decoded map[string]map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	if _, ok := decoded["configure"]["enabled"].(bool); !ok {
		t.Fatal("rpcd infers types from JSON values, not type-name strings")
	}
	if _, ok := decoded["history"]["limit"].(float64); !ok {
		t.Fatal("history limit must advertise integer")
	}
}
