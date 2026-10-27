package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "private", "control.sock")
}

func waitSocket(t *testing.T, path string) {
	t.Helper()
	for n := 0; n < 100; n++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("socket not ready")
}
func TestPrivateBridge(t *testing.T) {
	path := socketPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, path, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			return map[string]bool{"running": true}, nil
		})
	}()
	waitSocket(t, path)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, err)
	}
	dir, _ := os.Stat(filepath.Dir(path))
	if dir.Mode().Perm() != 0700 {
		t.Fatal("unsafe socket directory")
	}
	data, err := Call(ctx, path, "devices", json.RawMessage(`{}`))
	if err != nil || string(data) != `{"running":true}` {
		t.Fatal(string(data), err)
	}
	if _, err := Call(ctx, path, "exec", json.RawMessage(`{}`)); err == nil {
		t.Fatal("arbitrary method accepted")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("socket not removed")
	}
}
func TestBridgeSanitizesErrors(t *testing.T) {
	path := socketPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, path, func(context.Context, string, json.RawMessage) (any, error) {
			return nil, errors.New("sensitive diagnostic")
		})
	}()
	waitSocket(t, path)
	_, err := Call(ctx, path, "settings", json.RawMessage(`{}`))
	if err == nil || err.Error() != "request failed" {
		t.Fatal("error leak", err)
	}
	cancel()
	<-done
}
func TestRejectUnsafeDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), filepath.Join(dir, "s"), func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }); err == nil {
		t.Fatal("unsafe socket directory")
	}
}
func TestDispatcherCancellation(t *testing.T) {
	queue := make(chan Command)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dispatch(queue)(ctx, "devices", json.RawMessage(`{}`)); err == nil {
		t.Fatal("cancellation ignored")
	}
}
