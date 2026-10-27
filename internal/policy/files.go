package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

var ioEOF = io.EOF

type limitedReader = io.LimitedReader

func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func privateDir(path string) error {
	f, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !f.IsDir() || f.Mode()&os.ModeSymlink != 0 || f.Mode().Perm()&0077 != 0 {
		return errors.New("journal parent must be a private real directory (0700)")
	}
	s, ok := f.Sys().(*syscall.Stat_t)
	if !ok || int(s.Uid) != os.Geteuid() {
		return errors.New("journal parent has unexpected owner")
	}
	return nil
}
func openPrivate(path string, create bool) (*os.File, error) {
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	if create {
		flags = syscall.O_RDWR | syscall.O_CREAT | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	}
	fd, err := syscall.Open(path, flags, 0600)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	s, err := f.Stat()
	if err == nil {
		st := s.Sys().(*syscall.Stat_t)
		if !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || int(st.Uid) != os.Geteuid() || st.Nlink != 1 {
			err = errors.New("journal must be singly-linked, owned, regular 0600 file")
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// check is the last drift check before rename, not a cross-process CAS.
func atomicFile(path string, b []byte, mode os.FileMode, uid, gid int, check func() error) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".policy-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	good := false
	defer func() {
		if !good {
			f.Close()
		}
	}()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if uid >= 0 {
		if err = f.Chown(uid, gid); err != nil {
			return err
		}
	}
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	good = true
	if check != nil {
		if err = check(); err != nil {
			return err
		}
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func regularConfig(path string) ([]byte, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	s, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !s.Mode().IsRegular() || s.Size() > 2<<20 || s.Sys().(*syscall.Stat_t).Nlink != 1 || s.Mode().Perm()&0022 != 0 || int(s.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return nil, nil, errors.New("wireless must be bounded regular singly-linked file")
	}
	b, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if len(b) > 2<<20 {
		return nil, nil, fmt.Errorf("wireless too large")
	}
	return b, s, err
}
