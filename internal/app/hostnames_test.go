package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/hostnames"
	"openwrt-wifi-guard/internal/rpc"
)

type hostnameProvider func(context.Context) (map[string]hostnames.Hint, error)

func (f hostnameProvider) Lookup(ctx context.Context) (map[string]hostnames.Hint, error) {
	return f(ctx)
}

func TestHostnameWorkerRefreshFailureAndOwnerResponsiveness(t *testing.T) {
	const mac = "02:00:00:00:00:01"
	const wired = "02:00:00:00:00:02"
	r := devices.New(10)
	r.Authorize(mac, "ap0", time.Now())
	d := r.State.Devices[mac]
	d.FriendlyName = "Manual"
	d.Trust = "trusted"
	d.DesiredDeny = true
	d.PolicyRevision = 7
	commands := make(chan rpc.Command, 16)
	responses := make(chan map[string]hostnames.Hint, 4)
	entered := make(chan struct{}, 4)
	provider := hostnameProvider(func(ctx context.Context) (map[string]hostnames.Hint, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case hints := <-responses:
			if hints == nil {
				return nil, errors.New("unavailable")
			}
			return hints, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	s, err := New(context.Background(), Options{Registry: r, Store: &fakeStore{settings: config.Settings{}}, Save: func(devices.State) error { return nil }, Commands: commands, Hostnames: provider, HostnameInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	// Provider is blocked, but the registry owner still answers RPC immediately.
	if _, err := request(s, "devices", `{}`); err != nil {
		t.Fatal(err)
	}
	responses <- map[string]hostnames.Hint{mac: {Name: "dhcp-phone"}, wired: {Name: "wired"}}
	pump := func(want string) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case c := <-commands:
				s.Handle(c)
				if d.Hostname == want {
					return
				}
			case <-timer.C:
				t.Fatalf("hostname = %q want %q", d.Hostname, want)
			}
		}
	}
	pump("dhcp-phone")
	if len(r.State.Devices) != 1 || d.DisplayName() != "Manual" || d.Trust != "trusted" || !d.DesiredDeny || d.PolicyRevision != 7 {
		t.Fatal("enrichment changed identity/policy or discovered wired client")
	}
	value, err := request(s, "devices", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(value)
	var output struct {
		Devices []devices.Device `json:"devices"`
	}
	if json.Unmarshal(b, &output) != nil || output.Devices[0].Hostname != "dhcp-phone" {
		t.Fatalf("RPC missing hostname: %s", b)
	}
	if got := s.decisionResult(*d, "ok").Name; got != "Manual" {
		t.Fatal(got)
	}
	d.FriendlyName = ""
	if got := s.decisionResult(*d, "ok").Name; got != "dhcp-phone" {
		t.Fatal(got)
	}
	responses <- map[string]hostnames.Hint{mac: {Name: "refreshed"}}
	pump("refreshed")
	responses <- nil
	pump("refreshed") // failed lookup retains the bounded cache
	responses <- map[string]hostnames.Hint{}
	pump("")
	if len(r.State.History) != 1 {
		t.Fatal("metadata refresh created lifecycle history")
	}
	// Close cancels an in-flight provider and a queued owner dispatch.
}

func TestHostnameWorkerCloseWithoutOwner(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued-owner", true: "blocked-provider"}[blocked], func(t *testing.T) {
			entered := make(chan struct{})
			provider := hostnameProvider(func(ctx context.Context) (map[string]hostnames.Hint, error) {
				close(entered)
				if blocked {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return map[string]hostnames.Hint{}, nil
			})
			s, err := New(context.Background(), Options{Registry: devices.New(10), Store: &fakeStore{}, Save: func(devices.State) error { return nil }, Commands: make(chan rpc.Command, 1), Hostnames: provider})
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			closed := make(chan struct{})
			go func() { s.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("hostname worker did not stop")
			}
		})
	}
}

func TestHostnameCacheFreshAlertAndExpiry(t *testing.T) {
	s, r, _, sender := setup(t, false)
	const mac = "02:00:00:00:00:09"
	s.hostnameCache = map[string]hostnames.Hint{mac: {Name: "fresh-phone", Expires: time.Now().Add(time.Minute)}}
	s.hostnameUntil = time.Now().Add(time.Minute)
	r.Authorize(mac, "ap0", time.Now())
	s.Alert(devices.Event{MAC: mac, At: time.Now(), Interface: "ap0"})
	if m := <-sender.messages; m.Name != "fresh-phone" {
		t.Fatalf("alert name: %q", m.Name)
	}
	s.hostnameCache[mac] = hostnames.Hint{Name: "old", Expires: time.Now().Add(-time.Second)}
	s.enrichHostnames()
	if r.State.Devices[mac].Hostname != "" {
		t.Fatal("expired lease retained")
	}
	s.hostnameCache[mac] = hostnames.Hint{Name: "static"}
	s.hostnameUntil = time.Now().Add(-time.Second)
	s.enrichHostnames()
	if r.State.Devices[mac].Hostname != "" {
		t.Fatal("stale cache retained")
	}
	if _, err := request(s, "_hostnames_refresh", `{}`); err == nil {
		t.Fatal("public caller accepted")
	}
}
