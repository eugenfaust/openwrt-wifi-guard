package storage

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryLockRejectsConcurrentOwnerAndReleases(t *testing.T) {
	path := filepath.Join(privateDir(t), "state.json")
	first, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := AcquireLock(path); err == nil {
		second.Close()
		t.Fatal("concurrent registry owner accepted")
	} else if !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireLock(path)
	if err != nil {
		t.Fatal("lock not released", err)
	}
	defer second.Close()
	st, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatal("unsafe lock permissions", st.Mode())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lock changed the registry")
	}
}

// The helper runs in a separate process so SIGKILL exercises kernel cleanup,
// not the application's graceful Close path.
func TestRegistryLockProcessHelper(t *testing.T) {
	path := os.Getenv("WIFI_GUARD_LOCK_HELPER")
	if path == "" {
		return
	}
	f, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestRegistryLockReleasedAfterSIGKILL(t *testing.T) {
	path := filepath.Join(privateDir(t), "state.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRegistryLockProcessHelper$")
	cmd.Env = append(os.Environ(), "WIFI_GUARD_LOCK_HELPER="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "locked\n" {
			t.Fatal("helper failed to acquire lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lock helper timed out")
	}
	before, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if f, err := AcquireLock(path); err == nil {
		f.Close()
		t.Fatal("second process accepted while owner alive")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	f, err := AcquireLock(path)
	if err != nil {
		t.Fatal("stale lock after SIGKILL", err)
	}
	defer f.Close()
	after, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("lock inode was replaced")
	}
}

func TestRegistryLockRejectsSymlinkAndWritableDirectory(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "state.json")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Fatal(err)
	}
	if l, err := AcquireLock(path); err == nil {
		l.Close()
		t.Fatal("symlink lock accepted")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "untouched" {
		t.Fatal("symlink target changed")
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if l, err := AcquireLock(path); err == nil {
		l.Close()
		t.Fatal("writable lock directory accepted")
	}
}
