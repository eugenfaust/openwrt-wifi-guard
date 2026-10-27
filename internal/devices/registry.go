// Package devices implements single-owner Wi-Fi client lifecycle state.
package devices

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

type Device struct {
	MAC            string    `json:"mac"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
	FriendlyName   string    `json:"friendly_name"`
	Trust          string    `json:"trust"`
	LastInterface  string    `json:"last_interface"`
	Online         bool      `json:"online"`
	ReviewNonce    uint64    `json:"review_nonce,omitempty"`
	ReviewScope    string    `json:"review_scope,omitempty"`
	ReviewStage    string    `json:"review_stage,omitempty"`
	ReviewIssued   time.Time `json:"review_issued,omitempty"`
	DesiredDeny    bool      `json:"desired_deny,omitempty"`
	PolicyRevision uint64    `json:"policy_revision,omitempty"`
	PolicyPending  bool      `json:"policy_pending,omitempty"`
}
type Event struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	MAC       string    `json:"mac"`
	Interface string    `json:"interface"`
}
type State struct {
	Version          int                `json:"version"`
	Initialized      bool               `json:"initialized"`
	BootstrapPending map[string]bool    `json:"bootstrap_pending,omitempty"`
	Devices          map[string]*Device `json:"devices"`
	History          []Event            `json:"history"`
}
type Registry struct {
	State State
	Limit int
	Dirty bool
	Fresh bool // LastSeen-only updates, checkpointed less frequently.
}

func NormalizeMAC(s string) (string, error) {
	m, err := net.ParseMAC(s)
	if err != nil || len(m) != 6 || m[0]&1 != 0 || strings.ReplaceAll(m.String(), ":", "") == "000000000000" {
		return "", fmt.Errorf("invalid unicast MAC")
	}
	return m.String(), nil
}
func New(limit int) *Registry {
	return &Registry{State: State{Version: 1, Devices: map[string]*Device{}, History: []Event{}}, Limit: limit}
}
func (r *Registry) CompleteBootstrap() {
	if !r.State.Initialized {
		r.State.Initialized = true
		r.Dirty = true
	}
}
func (r *Registry) record(kind, mac, iface string, at time.Time) {
	r.State.History = append(r.State.History, Event{at, kind, mac, iface})
	if len(r.State.History) > r.Limit {
		r.State.History = r.State.History[len(r.State.History)-r.Limit:]
	}
	r.Dirty = true
}

// Authorize returns true only for a previously unseen device after bootstrap.
func (r *Registry) Authorize(mac, iface string, at time.Time) bool {
	mac, err := NormalizeMAC(mac)
	if err != nil || iface == "" {
		return false
	}
	d, known := r.State.Devices[mac]
	if !known {
		d = &Device{MAC: mac, FirstSeen: at, Trust: "unknown"}
		r.State.Devices[mac] = d
		r.record("discovered", mac, iface, at)
	} else if !d.Online {
		r.record("connected", mac, iface, at)
	}
	if at.After(d.LastSeen) {
		d.LastSeen = at
		r.Fresh = true
	}
	if !d.Online || d.LastInterface != iface {
		r.Dirty = true
	}
	d.Online = true
	d.LastInterface = iface
	return !known && r.State.Initialized
}
func (r *Registry) Disconnect(mac, iface string, at time.Time) {
	mac, err := NormalizeMAC(mac)
	if err != nil {
		return
	}
	d := r.State.Devices[mac]
	if d != nil && d.Online && d.LastInterface == iface {
		d.Online = false
		r.record("disconnected", mac, iface, at)
	}
}

// Reconcile only marks devices offline for successfully queried interfaces.
// All positive observations are applied before negatives to handle band roaming.
func (r *Registry) Reconcile(snap map[string][]string, at time.Time) []Event {
	return r.ReconcileWithImports(snap, nil, at)
}

// ReconcileWithImports suppresses only the initial snapshot of pending APs.
func (r *Registry) ReconcileWithImports(snap map[string][]string, imports map[string]bool, at time.Time) []Event {
	var alerts []Event
	present := map[string]bool{}
	ifaces := make([]string, 0, len(snap))
	for iface := range snap {
		ifaces = append(ifaces, iface)
	}
	sort.Strings(ifaces)
	for _, iface := range ifaces {
		for _, raw := range snap[iface] {
			mac, err := NormalizeMAC(raw)
			if err != nil {
				continue
			}
			present[mac] = true
			if r.Authorize(mac, iface, at) && !imports[iface] {
				alerts = append(alerts, Event{at, "new_device", mac, iface})
			}
		}
	}
	for mac, d := range r.State.Devices {
		if _, queried := snap[d.LastInterface]; queried && !present[mac] {
			r.Disconnect(mac, d.LastInterface, at)
		}
	}
	return alerts
}
