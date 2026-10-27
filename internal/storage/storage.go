// Package storage provides versioned, atomic, permission-restricted state files.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"openwrt-wifi-guard/internal/devices"
	"os"
	"path/filepath"
	"syscall"
)

const MaxStateBytes int64 = 16 << 20

// PublishedError means rename succeeded, but directory durability could not be
// confirmed. Callers must not roll memory back to a state older than the file.
type PublishedError struct{ Err error }

func (e *PublishedError) Error() string { return "state published but durability confirmation failed" }
func (e *PublishedError) Unwrap() error { return e.Err }

// Private directories prevent another local account from replacing the registry.
// Refuse symlink targets and broad permissions rather than silently chmod-ing.
func securePath(path string, directory bool) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("state path must be owned by current user")
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("state path must not be a symlink")
	}
	if directory {
		if !st.IsDir() || st.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("state directory must not be group/world writable")
		}
	} else if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("state file must be regular and private (0600)")
	}
	return nil
}

func Load(path string, limit int) (*devices.Registry, error) {
	if err := securePath(filepath.Dir(path), true); err != nil {
		return nil, err
	}
	if err := securePath(path, false); err != nil {
		return nil, err
	}
	r := devices.New(limit)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > MaxStateBytes {
		return nil, fmt.Errorf("state exceeds size limit")
	}
	dec := json.NewDecoder(io.LimitReader(f, MaxStateBytes))
	if err := dec.Decode(&r.State); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("state has trailing data")
	}
	if r.State.Version != 1 || r.State.Devices == nil {
		return nil, fmt.Errorf("unsupported or incomplete state")
	}
	for mac, d := range r.State.Devices {
		normalized, err := devices.NormalizeMAC(mac)
		if err != nil || normalized != mac || d == nil || d.MAC != mac || d.FirstSeen.IsZero() || d.LastSeen.Before(d.FirstSeen) || d.LastInterface == "" {
			return nil, fmt.Errorf("invalid device state")
		}
		if err := validateReview(d); err != nil {
			return nil, err
		}
		switch d.Trust {
		case "unknown", "trusted", "blocked":
		default:
			return nil, fmt.Errorf("invalid trust state")
		}
	}
	if len(r.State.History) > limit {
		r.State.History = r.State.History[len(r.State.History)-limit:]
		r.Dirty = true
	}
	return r, nil
}
func Save(path string, state devices.State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := securePath(dir, true); err != nil {
		return err
	}
	if err := securePath(path, false); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(state); err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > MaxStateBytes {
		return fmt.Errorf("state exceeds size limit; previous registry preserved")
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return &PublishedError{Err: err}
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return &PublishedError{Err: err}
	}
	return nil
}
