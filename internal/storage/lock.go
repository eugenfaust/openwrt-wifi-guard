package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked means another live daemon owns the registry. This is independent
// of the RPC socket: disabling/changing RPC must not permit competing writers.
var ErrLocked = errors.New("registry is already in use by another wifi-guard process")

// AcquireLock reserves a registry before it is loaded or mutated. Keep the
// returned descriptor open until the monitor, RPC owner and final save finish.
// The lock inode is deliberately never unlinked; unlinking a held lock allows
// another process to create and lock a different inode at the same pathname.
// Kernel locks are released on process exit, including crash or SIGKILL;
// a leftover file does not retain a lock after process death or router reboot.
func AcquireLock(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := securePath(dir, true); err != nil {
		return nil, err
	}
	lockPath := path + ".lock"
	if err := securePath(lockPath, false); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot open registry lock")
	}
	fail := func(err error) (*os.File, error) { f.Close(); return nil, err }
	st, err := f.Stat()
	if err != nil {
		return fail(fmt.Errorf("cannot inspect registry lock"))
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return fail(fmt.Errorf("unsafe registry lock"))
	}
	// Verify that the descriptor still names the checked pathname before locking.
	named, err := os.Lstat(lockPath)
	if err != nil || !os.SameFile(st, named) {
		return fail(fmt.Errorf("registry lock changed"))
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return fail(ErrLocked)
		}
		return fail(fmt.Errorf("cannot acquire registry lock"))
	}
	return f, nil
}
