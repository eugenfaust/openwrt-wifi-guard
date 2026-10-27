package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookProbeRetryAfterIsSharedWithWrites(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":120}}`)
	}))
	defer server.Close()
	notifier, err := New(callbackOptions(server))
	if err != nil {
		t.Fatal(err)
	}
	notifier.pollCallbacks(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if notifier.SendTest(ctx) == nil || requests.Load() != 1 {
		t.Fatal("webhook cooldown was bypassed by a write")
	}
}

func TestCallbackPermanentAuthorizationFailureStopsPolling(t *testing.T) {
	for _, code := range []int{401, 403} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if strings.HasSuffix(r.URL.Path, "getWebhookInfo") {
					_, _ = io.WriteString(w, `{"ok":true,"result":{"url":""}}`)
					return
				}
				requests.Add(1)
				w.WriteHeader(code)
				_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":%d}`, code)
			}))
			defer server.Close()
			notifier, err := New(callbackOptions(server))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			notifier.pollCallbacks(ctx)
			if ctx.Err() != nil || requests.Load() != 1 {
				t.Fatal("permanent authorization failure kept polling")
			}
		})
	}
}
