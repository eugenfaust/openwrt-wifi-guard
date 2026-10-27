package rpc

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// Cancellation must reach the queued command while its owner is still busy.
func TestCallerCancellationCancelsQueuedMutation(t *testing.T) {
	path := socketPath(t)
	serverCtx, stopServer := context.WithCancel(context.Background())
	queue := make(chan Command, 1)
	serverDone := make(chan error, 1)
	go func() { serverDone <- Serve(serverCtx, path, Dispatch(queue)) }()
	t.Cleanup(func() {
		stopServer()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("server did not shut down")
		}
	})
	waitSocket(t, path)

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	callDone := make(chan error, 1)
	go func() {
		_, err := Call(callerCtx, path, "rename", json.RawMessage(`{"mac":"02:00:00:00:00:01","name":"cancelled"}`))
		callDone <- err
	}()
	// Retain the command without starting the owner mutation.
	var command Command
	select {
	case command = <-queue:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the owner queue")
	}
	cancelCaller()
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("cancelled caller succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not close the connection")
	}
	select {
	case <-command.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("disconnected caller left its queued mutation live")
	}
	// Match the owner's pre-mutation context guard; no rollback is assumed.
	mutated := false
	if command.Context.Err() == nil {
		mutated = true
	}
	if mutated {
		t.Fatal("owner started a cancelled mutation")
	}
}

func TestRequestContextCancellation(t *testing.T) {
	for _, reason := range []string{"disconnect", "buffered extra request", "extra byte", "shutdown"} {
		t.Run(reason, func(t *testing.T) {
			path := socketPath(t)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			started := make(chan struct{})
			cancelled := make(chan error, 1)
			go func() {
				done <- Serve(ctx, path, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
					close(started)
					<-ctx.Done()
					cancelled <- ctx.Err()
					return nil, ctx.Err()
				})
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(time.Second):
					t.Error("server did not shut down")
				}
			})
			waitSocket(t, path)
			conn, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			request := "{\"method\":\"devices\",\"params\":{}}\n"
			if reason == "buffered extra request" {
				request += request
			}
			if _, err := conn.Write([]byte(request)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("handler did not start")
			}
			switch reason {
			case "disconnect":
				conn.Close()
			case "extra byte":
				if _, err := conn.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				cancel()
			}
			select {
			case err := <-cancelled:
				if err != context.Canceled {
					t.Fatalf("context error = %v, want cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request context remained live")
			}
		})
	}
}

func TestCallMaximumRequestFraming(t *testing.T) {
	path := socketPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, path, func(ctx context.Context, _ string, params json.RawMessage) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return len(params), nil
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("server did not shut down after a successful call")
		}
	})
	waitSocket(t, path)
	base, err := json.Marshal(wireRequest{"rename", json.RawMessage(`{"name":""}`)})
	if err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(`{"name":"` + strings.Repeat("x", MaxRequest-len(base)-1) + `"}`)
	callCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	data, err := Call(callCtx, path, "rename", params)
	if err != nil {
		t.Fatal("valid maximum-size newline-framed call failed:", err)
	}
	var size int
	if err := json.Unmarshal(data, &size); err != nil || size != len(params) {
		t.Fatalf("params length = %s, want %d (decode: %v)", data, len(params), err)
	}
	params = json.RawMessage(`{"name":"` + strings.Repeat("x", MaxRequest-len(base)) + `"}`)
	if _, err := Call(callCtx, path, "rename", params); err == nil {
		t.Fatal("oversized request accepted")
	}
}

func TestShutdownClosesIdleClients(t *testing.T) {
	path := socketPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, path, func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })
	}()
	waitSocket(t, path)
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Ensure the server accepted the connection without a complete request.
	if _, err := conn.Write([]byte("{")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle client delayed daemon shutdown")
	}
}
