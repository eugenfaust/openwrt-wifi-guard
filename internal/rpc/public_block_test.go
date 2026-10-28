package rpc

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPublicBlockSchemaAndWriteOnlyACL(t *testing.T) {
	schema, ok := Methods()["block"]
	if !ok || !validMethod("block") || len(schema) != 3 || schema["mac"] != "" || schema["revision"] != 0 || schema["confirmed"] != false {
		t.Fatal("block requires MAC, revision and boolean confirmation")
	}
	raw, err := os.ReadFile("../../luci/root/usr/share/rpcd/acl.d/luci-app-wifi-guard.json")
	if err != nil {
		t.Fatal(err)
	}
	var acl map[string]struct {
		Read struct {
			Ubus map[string][]string `json:"ubus"`
		} `json:"read"`
		Write struct {
			Ubus map[string][]string `json:"ubus"`
		} `json:"write"`
	}
	if err = json.Unmarshal(raw, &acl); err != nil {
		t.Fatal(err)
	}
	permissions := acl["luci-app-wifi-guard"]
	for _, method := range permissions.Read.Ubus["wifi-guard"] {
		if method == "block" {
			t.Fatal("read ACL grants block")
		}
	}
	found := false
	for _, method := range permissions.Write.Ubus["wifi-guard"] {
		if method == "block" {
			found = true
		}
		if len(method) > 0 && method[0] == '_' {
			t.Fatal("internal owner method granted")
		}
	}
	if !found {
		t.Fatal("block missing write ACL")
	}
}
