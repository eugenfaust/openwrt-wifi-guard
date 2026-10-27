package rpc

import "testing"

func TestDecisionMethodsKeepInternalOwnerCommandsPrivate(t *testing.T) {
	methods := Methods()
	for _, method := range []string{"_telegram_action", "_policy_check", "_policy_snapshot", "_policy_done"} {
		if validMethod(method) {
			t.Fatalf("internal command exposed over RPC: %s", method)
		}
	}
	schema, ok := methods["unblock"]
	if !ok || len(schema) != 2 || schema["mac"] != "" || schema["revision"] != 0 {
		t.Fatal("unblock must require explicit MAC and expected revision")
	}
}
