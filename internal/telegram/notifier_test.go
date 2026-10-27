package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const syntheticToken = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"

func options(server *httptest.Server) Options {
	return Options{Token: syntheticToken, ChatID: "-1001234567890", BaseURL: server.URL, Backoff: time.Millisecond, Timeout: time.Second}
}
func mustNew(t *testing.T, o Options) *Notifier {
	t.Helper()
	n, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
func TestFormatAndSuccess(t *testing.T) {
	m := Message{MAC: "02:00:00:00:00:01", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("test", 3600)), Interface: "phy1-ap0"}
	want := "New Wi-Fi device\nMAC: 02:00:00:00:00:01\nTime: 2026-01-02T02:04:05Z\nInterface: phy1-ap0"
	if got := Format(m); got != want {
		t.Fatalf("format = %q", got)
	}
	if got := Format(Message{Test: true, MAC: m.MAC, Interface: m.Interface}); !strings.Contains(got, "TEST") || strings.Contains(got, m.MAC) || strings.Contains(got, "Interface:") {
		t.Fatalf("test format = %q", got)
	}
	requests := make(chan string, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/bot"+syntheticToken+"/sendMessage" {
			t.Error("wrong method/path")
		}
		var payload struct {
			ChatID    string `json:"chat_id"`
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
			Preview   struct {
				Disabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.ChatID != "-1001234567890" || payload.ParseMode != "" || !payload.Preview.Disabled {
			t.Error("unsafe payload options")
		}
		requests <- payload.Text
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer s.Close()
	n := mustNew(t, options(s))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	if !n.Enqueue(m) {
		t.Fatal("enqueue failed")
	}
	select {
	case got := <-requests:
		if got != want {
			t.Error("wrong worker text")
		}
	case <-time.After(time.Second):
		t.Fatal("worker stalled")
	}
	cancel()
	<-done
	if n.Enqueue(m) {
		t.Fatal("accepted after stop")
	}
}
func TestRetryClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		want    int
		success bool
	}{
		{"server", 503, `untrusted body`, 3, false},
		{"rate limit", 429, `{"ok":false,"parameters":{"retry_after":0}}`, 3, false},
		{"api rate limit", 200, `{"ok":false,"error_code":429,"parameters":{"retry_after":0}}`, 3, false},
		{"terminal", 403, `{"ok":false}`, 1, false},
		{"bad json", 200, `{`, 1, false},
		{"missing result", 200, `{"ok":true}`, 1, false},
		{"zero message", 200, `{"ok":true,"result":{"message_id":0}}`, 1, false},
		{"missing ok", 200, `{"result":{"message_id":42}}`, 1, false},
		{"trailing garbage", 200, `{"ok":true,"result":{"message_id":42}} trailing`, 1, false},
		{"success", 200, `{"ok":true,"result":{"message_id":42}}`, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			n := mustNew(t, options(s))
			err := n.SendTest(context.Background())
			if (err == nil) != tc.success {
				t.Fatalf("unexpected error: %v", err)
			}
			if int(calls.Load()) != tc.want {
				t.Fatalf("calls=%d want=%d", calls.Load(), tc.want)
			}
		})
	}
}
func TestRetryEventuallySucceedsAndBackoff(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		count := len(times)
		mu.Unlock()
		if count < 3 {
			w.WriteHeader(500)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer s.Close()
	o := options(s)
	o.Backoff = 20 * time.Millisecond
	n := mustNew(t, o)
	if err := n.SendTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(times) != 3 || times[1].Sub(times[0]) < o.Backoff || times[2].Sub(times[1]) < 2*o.Backoff {
		t.Fatal("backoff not applied")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTransportErrorsAreSecretAndRetried(t *testing.T) {
	var calls atomic.Int32
	var logs bytes.Buffer
	o := Options{Token: syntheticToken, ChatID: "12345", Backoff: time.Millisecond, Logger: slog.New(slog.NewTextHandler(&logs, nil)), Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: errors.New("raw-response-secret")}
	})}}
	n := mustNew(t, o)
	err := n.SendTest(context.Background())
	if err == nil || calls.Load() != 3 {
		t.Fatal("transport retry failed")
	}
	assertSecret(t, err.Error()+logs.String())
}
func assertSecret(t *testing.T, s string) {
	t.Helper()
	for _, secret := range []string{syntheticToken, "http://", "https://", "raw-response-secret", "api.telegram.org"} {
		if strings.Contains(s, secret) {
			t.Fatal("secret leaked")
		}
	}
}
func TestCancellationAndTimeout(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer s.Close()
	defer close(release)
	o := options(s)
	o.Timeout = 10 * time.Millisecond
	n := mustNew(t, o)
	start := time.Now()
	err := n.SendTest(context.Background())
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("timeout not bounded")
	}
	assertSecret(t, err.Error())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = n.SendTest(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	assertSecret(t, err.Error())
}
func TestRateLimitWaitCancellation(t *testing.T) {
	called := make(chan struct{}, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"ok":false,"parameters":{"retry_after":999999999}}`)
		called <- struct{}{}
	}))
	defer s.Close()
	n := mustNew(t, options(s))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- n.SendTest(ctx) }()
	<-called
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry wait ignored cancellation")
	}
}
func TestServerRetryAfterNotClamped(t *testing.T) {
	for _, seconds := range []int64{-9223372036854775808, -1, 0, 1, 30, 31, 120, 9223372036, 9223372037, 9223372036854775807} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(429)
				fmt.Fprintf(w, `{"ok":false,"parameters":{"retry_after":%d}}`, seconds)
			}))
			defer s.Close()
			n := mustNew(t, options(s))
			_, after, err := n.attempt(context.Background(), Message{})
			want := time.Duration(1<<63 - 1)
			if seconds <= 0 {
				want = 0
			} else if seconds <= int64(want/time.Second) {
				want = time.Duration(seconds) * time.Second
			}
			if err == nil || after != want {
				t.Fatalf("retry_after=%d: delay=%v want=%v err=%v", seconds, after, want, err)
			}
		})
	}
}

func TestFinalAttemptCooldownAppliesToSendTest(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		fmt.Fprint(w, `{"ok":false,"parameters":{"retry_after":120}}`)
	}))
	defer s.Close()
	o := options(s)
	o.MaxAttempts = 1
	n := mustNew(t, o)
	if err := n.SendTest(context.Background()); err == nil {
		t.Fatal("expected rejection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := n.SendTest(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cooldown wait = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("sent during cooldown: %d requests", calls.Load())
	}
}

func TestCooldownWithVirtualClock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		attempts int
		queued   int
		seedTest bool
	}{
		{"current retry", 429, 2, 1, false},
		{"next queued message", 429, 1, 2, false},
		{"API current retry", 200, 2, 1, false},
		{"API next queued message", 200, 1, 2, false},
		{"test to queued message", 429, 1, 1, true},
		{"terminal response retains minimum", 403, 1, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			var mu sync.Mutex
			now := start
			var times []time.Time
			requests := make(chan struct{}, 3)
			o := Options{Token: syntheticToken, ChatID: "123", MaxAttempts: tc.attempts,
				Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					mu.Lock()
					times = append(times, now)
					count := len(times)
					mu.Unlock()
					requests <- struct{}{}
					status, body := 200, `{"ok":true,"result":{"message_id":1}}`
					if count == 1 {
						status, body = tc.status, `{"ok":false,"error_code":429,"parameters":{"retry_after":120}}`
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})}}
			n := mustNew(t, o)
			n.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
			waits := make(chan time.Duration, 4)
			advance := make(chan time.Duration)
			n.sleep = func(ctx context.Context, d time.Duration) error {
				select {
				case waits <- d:
				case <-ctx.Done():
					return ctx.Err()
				}
				select {
				case elapsed := <-advance:
					mu.Lock()
					now = now.Add(elapsed)
					mu.Unlock()
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.seedTest {
				if err := n.SendTest(ctx); err == nil {
					t.Fatal("expected rate limit")
				}
			}
			done := make(chan struct{})
			defer func() { cancel(); <-done }()
			for i := 0; i < tc.queued; i++ {
				n.Enqueue(Message{})
			}
			go func() { n.Run(ctx); close(done) }()
			waitFor := func(want time.Duration) {
				t.Helper()
				select {
				case got := <-waits:
					if got != want {
						t.Fatalf("wait=%v want=%v", got, want)
					}
				case <-time.After(time.Second):
					t.Fatal("worker did not wait")
				}
			}
			waitFor(120 * time.Second)
			// A concurrent test cannot bypass the worker's cooldown/gate.
			testCtx, testCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			if err := n.SendTest(testCtx); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("test bypassed cooldown: %v", err)
			}
			testCancel()
			// Even a prematurely completed sleep must not permit a request.
			advance <- 119 * time.Second
			waitFor(time.Second)
			mu.Lock()
			count := len(times)
			mu.Unlock()
			if count != 1 {
				t.Fatalf("early request count=%d", count)
			}
			advance <- time.Second
			// Acquire the same gate after the worker finishes, also proving a
			// later explicit test is allowed once the deadline expires.
			if err := n.SendTest(context.Background()); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				select {
				case <-requests:
				case <-time.After(time.Second):
					t.Fatal("missing request after cooldown")
				}
			}
			cancel()
			<-done
			mu.Lock()
			defer mu.Unlock()
			if len(times) != 3 || times[1].Sub(start) < 120*time.Second || times[2].Sub(start) < 120*time.Second {
				t.Fatalf("requests at %v", times)
			}
		})
	}
}

func TestCancelledWorkerRetainsCooldown(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		fmt.Fprint(w, `{"ok":false,"parameters":{"retry_after":120}}`)
	}))
	defer s.Close()
	n := mustNew(t, options(s))
	waiting := make(chan struct{}, 2)
	n.sleep = func(ctx context.Context, d time.Duration) error {
		waiting <- struct{}{}
		return sleepContext(ctx, d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan struct{})
	n.Enqueue(Message{})
	go func() { n.Run(ctx); close(workerDone) }()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter cooldown")
	}
	cancel()
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker cancellation stalled")
	}
	testCtx, testCancel := context.WithCancel(context.Background())
	defer testCancel()
	testDone := make(chan error, 1)
	go func() { testDone <- n.SendTest(testCtx) }()
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("test did not inherit cooldown")
	}
	testCancel()
	select {
	case err := <-testDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("test cancellation stalled")
	}
	if calls.Load() != 1 {
		t.Fatalf("request during inherited cooldown: %d", calls.Load())
	}
}

func TestOwnBackoffRemainsBounded(t *testing.T) {
	for _, initial := range []time.Duration{10 * time.Second, time.Duration(1<<63 - 1)} {
		o := Options{Token: syntheticToken, ChatID: "123", Backoff: initial, MaxAttempts: 5,
			Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
			})}}
		n := mustNew(t, o)
		var waits []time.Duration
		n.sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
		if err := n.SendTest(context.Background()); err == nil {
			t.Fatal("expected service failure")
		}
		want := []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second}
		if initial > maxDelay {
			want = []time.Duration{maxDelay, maxDelay, maxDelay, maxDelay}
		}
		if !reflect.DeepEqual(waits, want) {
			t.Fatalf("backoff=%v want=%v", waits, want)
		}
	}
}

func TestQueueOverflowAndConcurrentStop(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`) }))
	defer s.Close()
	o := options(s)
	o.Capacity = 2
	n := mustNew(t, o)
	if !n.Enqueue(Message{}) || !n.Enqueue(Message{}) || n.Enqueue(Message{}) {
		t.Fatal("queue capacity not enforced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); n.Run(ctx) }()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			n.Enqueue(Message{})
		}
	}()
	cancel()
	wg.Wait()
	n.Run(context.Background())
	if n.Enqueue(Message{}) {
		t.Fatal("enqueue after stop")
	}
}
func TestRedirectDoesNotLeak(t *testing.T) {
	var destination atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destination.Add(1) }))
	defer other.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL+"/"+syntheticToken, 307) }))
	defer s.Close()
	o := options(s)
	o.Client = s.Client()
	n := mustNew(t, o)
	err := n.SendTest(context.Background())
	if err == nil || destination.Load() != 0 {
		t.Fatal("redirect followed")
	}
	assertSecret(t, err.Error())
}
func TestValidationAndDefaults(t *testing.T) {
	for _, o := range []Options{{}, {Token: syntheticToken, ChatID: ""}, {Token: "invalid", ChatID: "123"}, {Token: syntheticToken + "/x", ChatID: "123"}, {Token: syntheticToken, ChatID: "0"}, {Token: syntheticToken, ChatID: "12\n34"}, {Token: syntheticToken, ChatID: "123", BaseURL: "http://example.invalid"}, {Token: syntheticToken, ChatID: "123", Capacity: -1}} {
		if _, err := New(o); err == nil {
			t.Fatal("invalid options accepted")
		} else {
			assertSecret(t, err.Error())
		}
	}
	n := mustNew(t, Options{Token: syntheticToken, ChatID: "123"})
	if n.baseURL != "https://api.telegram.org" || n.timeout != 8*time.Second || n.backoff != time.Second || n.attempts != 3 {
		t.Fatal("incorrect delivery defaults")
	}
	for i := 0; i < 32; i++ {
		if !n.Enqueue(Message{}) {
			t.Fatal("wrong default capacity")
		}
	}
	if n.Enqueue(Message{}) {
		t.Fatal("unbounded capacity")
	}
}
