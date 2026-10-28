// Package rpc provides a root-private Unix bridge for restricted rpcd methods.
// It never listens on TCP; LuCI session/ACL enforcement belongs to native rpcd.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const DefaultSocket = "/var/run/wifi-guard/control.sock"
const MaxRequest = 4096
const maxResponse = 16 << 20

type Handler func(context.Context, string, json.RawMessage) (any, error)
type Result struct {
	Value any
	Err   error
}
type Command struct {
	Context context.Context
	Method  string
	Params  json.RawMessage
	Reply   chan Result
}

func Dispatch(queue chan<- Command) Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		reply := make(chan Result, 1)
		request := Command{ctx, method, params, reply}
		select {
		case queue <- request:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case r := <-reply:
			return r.Value, r.Err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// rpcd executable schemas infer types from exemplar JSON values, not names.
func Methods() map[string]map[string]any {
	return map[string]map[string]any{
		"devices": {}, "history": {"limit": 32}, "settings": {},
		"rename": {"mac": "", "name": ""}, "trust": {"mac": "", "trust": ""},
		"unblock":   {"mac": "", "revision": 0},
		"block":     {"mac": "", "revision": 0, "confirmed": false},
		"configure": {"enabled": true, "chat_id": "", "token": "", "clear_token": true}, "test": {},
	}
}
func validMethod(s string) bool { _, ok := Methods()[s]; return ok }

type wireRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type wireResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || !st.IsDir() || st.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("unsafe RPC directory")
	}
	return nil
}
func Serve(parent context.Context, path string, handle Handler) error {
	if handle == nil {
		return fmt.Errorf("RPC handler required")
	}
	if err := privateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil {
		owner, ok := st.Sys().(*syscall.Stat_t)
		if st.Mode()&os.ModeSocket == 0 || !ok || owner.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("unsafe RPC socket")
		}
		conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return fmt.Errorf("RPC socket already active")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return fmt.Errorf("RPC socket inaccessible")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		l.Close()
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	defer func() { cancel(); l.Close(); wg.Wait() }()
	go func() { <-ctx.Done(); l.Close() }()
	slots := make(chan struct{}, 8)
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("RPC accept failed")
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer conn.Close()
			closeOnCancel := context.AfterFunc(ctx, func() { conn.Close() })
			defer closeOnCancel()
			conn.SetDeadline(time.Now().Add(35 * time.Second))
			// The OS pathname permissions authenticate local peers. Bound every request
			// before JSON decoding and never log payloads (configure contains a secret).
			reader := bufio.NewReader(io.LimitReader(conn, MaxRequest+1))
			line, err := reader.ReadBytes('\n')
			var request wireRequest
			if err != nil || len(line) > MaxRequest || json.Unmarshal(line, &request) != nil || !validMethod(request.Method) {
				_ = json.NewEncoder(conn).Encode(wireResponse{Error: "invalid request"})
				return
			}
			callCtx, stop := context.WithTimeout(ctx, 30*time.Second)
			defer stop()
			// Exactly one newline-framed request is allowed per connection. Reuse
			// the buffered reader so even already-buffered extra bytes cancel it.
			// EOF/read failure means the caller disconnected; extra data is a
			// protocol violation. Neither requires draining an unbounded stream.
			peerDone := make(chan struct{})
			go func() {
				defer close(peerDone)
				_, _ = reader.ReadByte()
				stop()
			}()
			defer func() {
				conn.Close()
				<-peerDone
			}()
			// Owners must check cancellation before starting a queued mutation.
			// Once started, cancellation is best effort and does not imply rollback.
			value, err := handle(callCtx, request.Method, request.Params)
			response := wireResponse{}
			if err != nil {
				response.Error = "request failed"
			} else {
				data, e := json.Marshal(value)
				if e != nil || len(data) > maxResponse {
					response.Error = "request failed"
				} else {
					response.Result = data
				}
			}
			_ = json.NewEncoder(conn).Encode(response)
		}()
	}
}
func Call(ctx context.Context, path, method string, params json.RawMessage) (json.RawMessage, error) {
	if !validMethod(method) {
		return nil, fmt.Errorf("unknown RPC method")
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	request, err := json.Marshal(wireRequest{method, params})
	if err != nil || len(request)+1 > MaxRequest {
		return nil, fmt.Errorf("invalid RPC request")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("daemon unavailable")
	}
	defer conn.Close()
	deadline := time.Now().Add(34 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	// Context cancellation must interrupt an established idle connection as well.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err := conn.Write(append(request, '\n')); err != nil {
		return nil, fmt.Errorf("RPC request failed")
	}
	var response wireResponse
	if err := json.NewDecoder(io.LimitReader(conn, maxResponse+1024)).Decode(&response); err != nil {
		return nil, fmt.Errorf("invalid RPC response")
	}
	if response.Error != "" {
		return nil, fmt.Errorf("%s", response.Error)
	}
	if !json.Valid(response.Result) {
		return nil, fmt.Errorf("invalid RPC response")
	}
	return response.Result, nil
}
