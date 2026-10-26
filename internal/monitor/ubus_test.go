package monitor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ubus")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestUbusDiscoveryAndSubscription(t *testing.T) {
	p := script(t, `case "$1" in
 list) printf '%s\n' hostapd.ap1 hostapd.ap0 hostapd.ap1 network.interface.lan ;;
 call) printf '%s\n' '{"clients":{"02:00:00:00:00:01":{"authorized":true}}}' ;;
 subscribe) printf '%s\n' '{invalid' '{"probe":{"address":"02:00:00:00:00:01"}}' '{"sta-authorized":{"address":"02:00:00:00:00:02","ifname":"ap0"}}' ;;
 esac
 `)
	u := Ubus{p, time.Second}
	ctx := context.Background()
	objs, err := u.Discover(ctx)
	if err != nil || len(objs) != 2 || objs[0] != "hostapd.ap0" {
		t.Fatal(objs, err)
	}
	clients, err := u.Clients(ctx, objs[0])
	if err != nil || len(clients) != 1 {
		t.Fatal(clients, err)
	}
	events, done := u.Subscribe(ctx, objs)
	count := 0
	for e := range events {
		count++
		if e.Kind != "authorized" {
			t.Fatal(e)
		}
	}
	if count != 1 {
		t.Fatal("missing event", count)
	}
	if err := <-done; err == nil {
		t.Fatal("EOF should reconnect")
	}
}
func TestUbusIdleCancellationAndTimeout(t *testing.T) {
	p := script(t, "exec sleep 10\n")
	u := Ubus{p, 20 * time.Millisecond}
	start := time.Now()
	if _, err := u.Discover(context.Background()); err == nil {
		t.Fatal("missing timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatal("command timeout hung")
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, done := u.Subscribe(ctx, []string{"hostapd.ap0"})
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle subscription not killed")
	}
	for range events {
	}
}
