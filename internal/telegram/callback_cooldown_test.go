package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCallbackAndDeliveryShareCooldown(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "answerCallbackQuery"), strings.HasSuffix(r.URL.Path, "editMessageText"):
			w.WriteHeader(429)
			fmt.Fprint(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":3}}`)
		case strings.HasSuffix(r.URL.Path, "sendMessage"):
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
		default:
			t.Error("unexpected method", r.URL.Path)
		}
	}))
	defer s.Close()
	o := callbackOptions(s)
	o.HandleAction = func(context.Context, ActionClaims) (ActionResult, error) { return ActionResult{Text: "Warning"}, nil }
	n := mustNew(t, o)
	now := time.Now()
	n.now = func() time.Time { return now }
	var waits []time.Duration
	n.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); now = now.Add(d); return nil }
	if !n.processCallback(context.Background(), callbackFixture(t, n)) {
		t.Fatal("UI failure replayed committed action")
	}
	if err := n.SendTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || waits[0] != 3*time.Second || waits[1] != 3*time.Second {
		t.Fatalf("lost shared cooldown: %v", waits)
	}
	// Cancellation during a cooldown must release the gate for the next writer.
	n.cooldown = now.Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	n.sleep = func(context.Context, time.Duration) error { cancel(); return ctx.Err() }
	if err := n.callbackGate(ctx); err == nil {
		t.Fatal("cancelled cooldown accepted")
	}
	if len(n.gate) != 0 {
		t.Fatal("gate leaked after cancellation")
	}
}
