// Package policy implements an opt-in, durable, target-only AP deny policy.
package policy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxJournalSize = 4 << 20

type Options struct {
	Path          string
	ConfigDir     string
	UCI           string
	UBus          string
	ControlDir    string
	ProtectedMACs []string
}
type Status struct {
	Desired bool   `json:"desired"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}
type ownership struct {
	Section      string `json:"section"`
	MAC          string `json:"mac"`
	OriginalMode string `json:"original_mode"`
}
type runtimeOwnership struct {
	ACL         bool  `json:"acl"`
	ExternalACL bool  `json:"external_acl,omitempty"`
	Ban         bool  `json:"ban"`
	BanUntil    int64 `json:"ban_until,omitempty"`
	Done        bool  `json:"done,omitempty"`
}
type entry struct {
	Desired bool                         `json:"desired"`
	Runtime map[string]*runtimeOwnership `json:"runtime"`
}
type pending struct {
	Before string      `json:"before"`
	After  string      `json:"after"`
	Owners []ownership `json:"owners"`
}
type journal struct {
	Version int               `json:"version"`
	Entries map[string]*entry `json:"entries"`
	Owners  []ownership       `json:"owners"`
	Pending *pending          `json:"pending,omitempty"`
}

// Engine serializes mutations; Status never waits for UCI or hostapd I/O.
// Keep one Engine open per journal, and call Close when its worker is stopped.
type Engine struct {
	mu        sync.Mutex
	statusMu  sync.RWMutex
	statuses  map[string]Status
	opts      Options
	protected map[string]bool
	state     journal
	backend   backend
	lock      *os.File
	fault     error
	saved     string
}

func normalize(s string) (string, error) {
	m, e := net.ParseMAC(s)
	if e != nil || len(m) != 6 || m[0]&1 != 0 {
		return "", errors.New("invalid unicast MAC")
	}
	if m.String() == "00:00:00:00:00:00" {
		return "", errors.New("zero MAC is unsupported")
	}
	return m.String(), nil
}
func Open(o Options) (*Engine, error) {
	if o.Path == "" {
		return nil, errors.New("policy journal path is required")
	}
	if o.ConfigDir == "" {
		o.ConfigDir = "/etc/config"
	}
	if o.UCI == "" {
		o.UCI = "uci"
	}
	if o.UBus == "" {
		o.UBus = "ubus"
	}
	if o.ControlDir == "" {
		o.ControlDir = "/var/run/hostapd"
	}
	e := &Engine{opts: o, protected: map[string]bool{}, statuses: map[string]Status{}}
	for _, s := range o.ProtectedMACs {
		m, err := normalize(s)
		if err != nil {
			return nil, fmt.Errorf("protected MAC: %w", err)
		}
		e.protected[m] = true
	}
	if err := privateDir(filepath.Dir(o.Path)); err != nil {
		return nil, err
	}
	l, err := openPrivate(o.Path+".lock", true)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(l.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		l.Close()
		return nil, errors.New("policy journal already locked")
	}
	e.lock = l
	e.state = journal{Version: 1, Entries: map[string]*entry{}}
	f, err := openPrivate(o.Path, false)
	if err == nil {
		defer f.Close()
		info, statErr := f.Stat()
		if statErr != nil || info.Size() > maxJournalSize {
			e.Close()
			return nil, errors.New("policy journal exceeds size limit or cannot be inspected")
		}
		d := json.NewDecoder(&limitedReader{R: f, N: 4 << 20})
		d.DisallowUnknownFields()
		err = d.Decode(&e.state)
		if err == nil {
			var extra any
			if d.Decode(&extra) != ioEOF {
				err = errors.New("trailing journal data")
			}
		}
	} else if os.IsNotExist(err) {
		err = nil
	}
	if err == nil {
		err = e.validate()
	}
	if err != nil {
		e.Close()
		return nil, fmt.Errorf("policy journal: %w", err)
	}
	for m, v := range e.state.Entries {
		e.statuses[m] = Status{Desired: v.Desired, Error: "reconciliation required"}
	}
	e.backend = &systemBackend{opts: o}
	return e, nil
}
func (e *Engine) validate() error {
	if e.state.Version != 1 || e.state.Entries == nil {
		return errors.New("unsupported journal")
	}
	for m, v := range e.state.Entries {
		n, err := normalize(m)
		if err != nil || n != m || v == nil || v.Runtime == nil {
			return errors.New("invalid policy entry")
		}
		for i, r := range v.Runtime {
			if !safeName(i) || r == nil {
				return errors.New("invalid runtime ownership")
			}
		}
	}
	check := func(owners []ownership) error {
		seen := map[string]bool{}
		modes := map[string]string{}
		for _, o := range owners {
			key := o.Section + "/" + o.MAC
			if seen[key] {
				return errors.New("duplicate UCI ownership")
			}
			seen[key] = true
			if mode, ok := modes[o.Section]; ok && mode != o.OriginalMode {
				return errors.New("inconsistent original UCI mode")
			}
			modes[o.Section] = o.OriginalMode
			if !sectionName(o.Section) || e.state.Entries[o.MAC] == nil || !(o.OriginalMode == "" || o.OriginalMode == "disable" || o.OriginalMode == "deny") {
				return errors.New("invalid UCI ownership")
			}
		}
		return nil
	}
	if err := check(e.state.Owners); err != nil {
		return err
	}
	if p := e.state.Pending; p != nil {
		_, beforeErr := hex.DecodeString(p.Before)
		_, afterErr := hex.DecodeString(p.After)
		if len(p.Before) != 64 || len(p.After) != 64 || beforeErr != nil || afterErr != nil {
			return errors.New("invalid publication journal")
		}
		return check(p.Owners)
	}
	return nil
}
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lock == nil {
		return nil
	}
	err := e.lock.Close()
	e.lock = nil
	return err
}
func (e *Engine) Status(mac string) Status {
	m, err := normalize(mac)
	if err != nil {
		return Status{Error: err.Error()}
	}
	e.statusMu.RLock()
	defer e.statusMu.RUnlock()
	return e.statuses[m]
}
func (e *Engine) SetDenied(ctx context.Context, mac string, deny bool) (Status, error) {
	m, err := normalize(mac)
	if err != nil {
		return Status{Error: err.Error()}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err = ctx.Err(); err == nil && deny {
		if len(e.protected) == 0 {
			err = errors.New("protected MAC list is required")
		} else if e.protected[m] {
			err = errors.New("protected MAC cannot be denied")
		}
	}
	if err != nil {
		return e.Status(m), err
	}
	if e.lock == nil {
		return e.Status(m), errors.New("engine closed")
	}
	if e.fault != nil {
		return e.Status(m), e.fault
	}
	// Never replace an unresolved publication's ownership with new intent.
	if e.state.Pending != nil {
		if err = e.recover(ctx); err != nil {
			return e.Status(m), err
		}
	}
	v := e.state.Entries[m]
	if v == nil && !deny {
		s := Status{Applied: true}
		e.setStatus(m, s)
		return s, nil
	}
	if v == nil {
		v = &entry{Runtime: map[string]*runtimeOwnership{}}
		e.state.Entries[m] = v
	}
	v.Desired = deny
	if err = e.save(); err == nil {
		e.setStatus(m, Status{Desired: deny, Error: "reconciliation required"})
		err = e.reconcile(ctx)
	}
	return e.Status(m), err
}
func (e *Engine) Reconcile(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lock == nil {
		return errors.New("engine closed")
	}
	if e.fault != nil {
		return e.fault
	}
	return e.reconcile(ctx)
}
func (e *Engine) setStatus(m string, s Status) {
	e.statusMu.Lock()
	e.statuses[m] = s
	e.statusMu.Unlock()
}
func (e *Engine) save() error {
	b, err := json.Marshal(e.state)
	if err == nil && len(b) > maxJournalSize {
		err = errors.New("policy journal exceeds size limit")
	}
	if err == nil && string(b) == e.saved {
		return nil
	}
	if err == nil {
		err = atomicFile(e.opts.Path, b, 0600, -1, -1, nil)
		if err == nil {
			e.saved = string(b)
		}
	}
	if err != nil {
		e.fault = fmt.Errorf("journal durability uncertain; close and reopen: %w", err)
	}
	return err
}
func (e *Engine) recover(ctx context.Context) error {
	p := e.state.Pending
	if p == nil {
		return nil
	}
	h, err := e.backend.currentHash(ctx)
	if err != nil {
		return err
	}
	switch h {
	case p.After:
		e.state.Owners = p.Owners
	case p.Before:
	default:
		return errors.New("wireless drift during pending publication; manual recovery required")
	}
	e.state.Pending = nil
	return e.save()
}
func (e *Engine) reconcile(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			for m, v := range e.state.Entries {
				e.setStatus(m, Status{Desired: v.Desired, Error: err.Error()})
			}
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = e.recover(ctx); err != nil {
		return err
	}
	work := len(e.state.Owners) > 0
	for _, v := range e.state.Entries {
		work = work || v.Desired
		for _, r := range v.Runtime {
			work = work || r.ACL || r.Ban
		}
	}
	if !work {
		for m := range e.state.Entries {
			e.setStatus(m, Status{Applied: true})
		}
		return nil
	}
	for m, v := range e.state.Entries {
		if v.Desired && (len(e.protected) == 0 || e.protected[m]) {
			return errors.New("saved deny conflicts with protection configuration")
		}
	}
	plan, err := e.backend.prepare(ctx, e.state)
	if err != nil {
		return err
	}
	defer plan.close()
	activeRuntime := map[string]bool{}
	for _, ap := range plan.aps {
		activeRuntime[ap.ifname] = true
	}
	for _, v := range e.state.Entries {
		for iface, r := range v.Runtime {
			if (r.ACL || r.Ban) && !activeRuntime[iface] {
				return errors.New("owned runtime AP unavailable; reconciliation required")
			}
		}
	}
	// Claim runtime copies loaded from our persistent UCI entries before removing
	// those entries. This intent survives a crash between publication and DEL_MAC.
	for _, o := range e.state.Owners {
		for _, ap := range plan.aps {
			if o.Section == ap.section && ap.persistentACL[o.MAC] {
				v := e.state.Entries[o.MAC]
				r := v.Runtime[ap.ifname]
				if r == nil {
					r = &runtimeOwnership{}
					v.Runtime[ap.ifname] = r
				}
				if !r.ExternalACL {
					r.ACL = true
				}
			}
		}
	}
	// Preserve pre-existing runtime rules before publishing new UCI ownership.
	// Otherwise a crash after rename could make an external ACL appear to be ours.
	for m, v := range e.state.Entries {
		for _, ap := range plan.aps {
			r := v.Runtime[ap.ifname]
			if r == nil {
				r = &runtimeOwnership{}
				v.Runtime[ap.ifname] = r
			}
			if ap.runtimeACL[m] && !r.ACL {
				r.ExternalACL = true
			}
		}
	}
	if err = e.save(); err != nil {
		return err
	}
	if plan.before != plan.after {
		e.state.Pending = &pending{Before: plan.before, After: plan.after, Owners: plan.owners}
		if err = e.save(); err != nil {
			return err
		}
		if err = plan.publish(ctx); err != nil {
			return err
		}
		if err = e.recover(ctx); err != nil {
			return err
		}
	} else {
		e.state.Owners = plan.owners
		if err = e.save(); err != nil {
			return err
		}
	}
	// All APs have been capability-checked before the first publication.
	for m, v := range e.state.Entries {
		for _, ap := range plan.aps {
			r := v.Runtime[ap.ifname]
			if r == nil {
				r = &runtimeOwnership{}
				v.Runtime[ap.ifname] = r
			}
			acl, err := e.backend.acl(ctx, ap.ifname)
			if err != nil {
				return err
			}
			bans := map[string]bool{}
			if r.Ban {
				bans, err = e.backend.bans(ctx, ap.ifname)
				if err != nil {
					return err
				}
			}
			if v.Desired {
				if acl[m] && !r.ACL && !r.ExternalACL {
					r.ExternalACL = true
					if err = e.save(); err != nil {
						return err
					}
				}
				// Existing external UCI/runtime entries are never claimed.
				if !acl[m] {
					r.ExternalACL = false
					r.ACL = true
					if err = e.save(); err != nil {
						return err
					}
				}
				if !acl[m] {
					if err = e.backend.control(ctx, ap.ifname, "DENY_ACL ADD_MAC "+strings.ToUpper(m)); err != nil {
						return err
					}
				}
				// The permanent ACL already prevents reconnects. Never create a transient
				// ubus ban: its MAC-only identity cannot prove ownership on removal.
				if !r.Done || !acl[m] {
					if err = e.backend.disconnect(ctx, ap.ifname, m); err != nil {
						return err
					}
					r.Done = true
					if err = e.save(); err != nil {
						return err
					}
				}
			} else {
				if r.ACL && acl[m] {
					// A concurrently added external persistent deny must not be removed from runtime.
					if ap.external[m] {
						return errors.New("external persistent deny prevents owned runtime removal")
					}
					if err = e.backend.control(ctx, ap.ifname, "DENY_ACL DEL_MAC "+strings.ToUpper(m)); err != nil {
						return err
					}
					got, readErr := e.backend.acl(ctx, ap.ifname)
					if readErr != nil {
						return readErr
					}
					if got[m] {
						return errors.New("runtime removal readback failed")
					}
				}
				if r.Ban && bans[m] && time.Now().UnixMilli() < r.BanUntil {
					return errors.New("legacy transient ban still active; retry after its recorded expiry")
				}
				r.ACL = false
				r.Ban = false
				r.BanUntil = 0
				r.Done = false
				if err = e.save(); err != nil {
					return err
				}
			}
			got, err := e.backend.acl(ctx, ap.ifname)
			if err != nil {
				return err
			}
			if v.Desired && !got[m] {
				return errors.New("runtime deny readback failed")
			}

		}
		e.setStatus(m, Status{Desired: v.Desired, Applied: true})
	}
	return nil
}
