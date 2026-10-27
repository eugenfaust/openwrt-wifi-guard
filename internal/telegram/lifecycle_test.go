package telegram

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryAfterAndClamp(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"ok":false,"parameters":{"retry_after":1}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer s.Close()
	n := mustNew(t, options(s))
	start := time.Now()
	if err := n.SendTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Second {
		t.Fatal("retry_after ignored")
	}
	if boundedDelay(time.Hour) != 30*time.Second {
		t.Fatal("backoff clamp failed")
	}
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"ok":false,"parameters":{"retry_after":9223372036854775807}}`)
	}))
	defer huge.Close()
	n = mustNew(t, options(huge))
	retry, after, err := n.attempt(context.Background(), Message{Test: true})
	if !retry || after != time.Duration(1<<63-1) || err == nil {
		t.Fatalf("bad rate limit saturation: %v %s", retry, after)
	}
}

func TestWorkerFailureLogsAreSecret(t *testing.T) {
	var logs bytes.Buffer
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, syntheticToken+" raw-response-secret "+r.URL.String())
	}))
	defer s.Close()
	o := options(s)
	o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	n := mustNew(t, o)
	// Wait for a subsequent successful queue dequeue via the transport before stop.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if !n.Enqueue(Message{}) {
		t.Fatal("enqueue failed")
	}
	n.Run(ctx)
	if !strings.Contains(logs.String(), "Telegram notification failed") {
		t.Fatal("missing failure log")
	}
	assertSecret(t, logs.String())
}

func TestSequentialWorkerAndSendTest(t *testing.T) {
	var active, max atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := max.Load(); current > previous; previous = max.Load() {
			if max.CompareAndSwap(previous, current) {
				break
			}
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer s.Close()
	n := mustNew(t, options(s))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan struct{})
	n.Enqueue(Message{})
	go func() { n.Run(ctx); close(workerDone) }()
	<-entered
	testCtx, testCancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer testCancel()
	if err := n.SendTest(testCtx); err != context.DeadlineExceeded {
		t.Fatalf("gate cancellation = %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(4)
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			if err := n.SendTest(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	close(release)
	wg.Wait()
	cancel()
	<-workerDone
	if max.Load() != 1 {
		t.Fatal("requests were not sequential")
	}
}

func TestMalformedAndOversizedResponseSecrecy(t *testing.T) {
	for _, body := range []string{syntheticToken + " raw-response-secret", strings.Repeat("x", 65537), `{"ok":true,"result":{"message_id":"bad"}}`, `null`} {
		var calls atomic.Int32
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, body) }))
		n := mustNew(t, options(s))
		err := n.SendTest(context.Background())
		s.Close()
		if err == nil || calls.Load() != 1 {
			t.Fatal("malformed success retried or accepted")
		}
		assertSecret(t, err.Error())
	}
}

func TestCancelledWorkerDoesNotDrainQueue(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer s.Close()
	n := mustNew(t, options(s))
	n.Enqueue(Message{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n.Run(ctx)
	if calls.Load() != 0 || n.Enqueue(Message{}) {
		t.Fatal("cancelled worker sent or accepted work")
	}
}
