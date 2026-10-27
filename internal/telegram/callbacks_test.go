package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func callbackFixture(t *testing.T, n *Notifier) callbackUpdate {
	t.Helper()
	token, err := n.EncodeAction(ActionClaims{Action: ActionTrust, MAC: "02:00:00:00:00:01", Nonce: 42, Issued: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var u callbackUpdate
	raw := fmt.Sprintf(`{"update_id":5,"callback_query":{"id":"query","data":%q,"from":{"id":12345,"is_bot":false},"message":{"message_id":9,"date":1,"chat":{"id":12345,"type":"private"}}}}`, token)
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	return u
}
func callbackOptions(s *httptest.Server) Options {
	o := options(s)
	o.ChatID = "12345"
	o.Callbacks = true
	o.Backoff = time.Millisecond
	o.HandleAction = func(context.Context, ActionClaims) (ActionResult, error) { return ActionResult{}, nil }
	return o
}
func TestCallbackAuthenticationAndDelegation(t *testing.T) {
	var answers, edits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "answerCallbackQuery") {
			answers.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "editMessageText") {
			edits.Add(1)
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	defer s.Close()
	o := callbackOptions(s)
	var handled int
	o.HandleAction = func(_ context.Context, c ActionClaims) (ActionResult, error) {
		handled++
		if c.Nonce != 42 || c.Action != ActionTrust {
			t.Fatal(c)
		}
		return ActionResult{Text: "Trusted", Keyboard: [][]InlineButton{}}, nil
	}
	n := mustNew(t, o)
	for _, mutate := range []func(*callbackUpdate){
		func(u *callbackUpdate) { u.Query = nil },
		func(u *callbackUpdate) { u.Query.ID = "" },
		func(u *callbackUpdate) { u.Query.From.ID++ },
		func(u *callbackUpdate) { u.Query.From.Bot = true },
		func(u *callbackUpdate) { u.Query.Message.Chat.Type = "group" },
		func(u *callbackUpdate) { u.Query.Message.Chat.ID++ },
		func(u *callbackUpdate) { u.Query.Message.Date = 0 },
		func(u *callbackUpdate) { u.Query.Message.ID = 0 },
		func(u *callbackUpdate) { u.Query.Message = nil },
		func(u *callbackUpdate) { u.Query.InlineMessageID = "inline" },
		func(u *callbackUpdate) { u.Query.Data = "x" + u.Query.Data[1:] },
		func(u *callbackUpdate) {
			u.Query.Data, _ = n.codec.Encode(ActionClaims{Action: ActionTrust, MAC: "02:00:00:00:00:01", Nonce: 42, Issued: time.Now()}, 54321)
		},
	} {
		u := callbackFixture(t, n)
		mutate(&u)
		if !n.processCallback(context.Background(), u) {
			t.Fatal("reject not terminal")
		}
	}
	u := callbackFixture(t, n)
	u.Query.Data, _ = n.EncodeAction(ActionClaims{Action: ActionTrust, MAC: "02:00:00:00:00:01", Nonce: 42, Issued: time.Now().Add(-8 * 24 * time.Hour)})
	n.processCallback(context.Background(), u)
	if handled != 0 || answers.Load() != 0 {
		t.Fatal("unauthorized callback answered or delegated")
	}
	// Replay intentionally reaches owner: only its durable nonce state can decide.
	for i := 0; i < 2; i++ {
		if !n.processCallback(context.Background(), callbackFixture(t, n)) {
			t.Fatal("success retried")
		}
	}
	if handled != 2 || answers.Load() != 2 || edits.Load() != 2 {
		t.Fatal("delegation/result keyboard failed")
	}
	n.handleAction = func(context.Context, ActionClaims) (ActionResult, error) {
		return ActionResult{}, errors.New(syntheticToken)
	}
	if n.processCallback(context.Background(), callbackFixture(t, n)) {
		t.Fatal("transient failure committed")
	}
}

func TestCallbacksFailClosedAndNoPollingFight(t *testing.T) {
	for _, mode := range []string{"webhook", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			var polls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "getWebhookInfo"):
					if mode == "webhook" {
						fmt.Fprint(w, `{"ok":true,"result":{"url":"https://example.invalid/hook"}}`)
					} else {
						fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
					}
				case strings.HasSuffix(r.URL.Path, "getUpdates"):
					polls.Add(1)
					w.WriteHeader(409)
					fmt.Fprint(w, `{"ok":false,"error_code":409}`)
				default:
					t.Error("unexpected method", r.URL.Path)
				}
			}))
			defer s.Close()
			n := mustNew(t, callbackOptions(s))
			n.pollCallbacks(context.Background())
			want := int32(0)
			if mode == "conflict" {
				want = 1
			}
			if polls.Load() != want {
				t.Fatal(polls.Load())
			}
		})
	}
}

func TestLongPollDoesNotBlockDeliveryAndJoins(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	release := make(chan struct{})
	// Ensure a failed assertion cannot deadlock httptest.Server.Close.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "getWebhookInfo"):
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case strings.HasSuffix(r.URL.Path, "getUpdates"):
			// net/http cannot detect peer closure while the POST body is unread.
			_, _ = io.Copy(io.Discard, r.Body)
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			close(exited)
		case strings.HasSuffix(r.URL.Path, "sendMessage"):
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
		default:
			t.Error("unexpected method")
		}
	}))
	defer func() { close(release); s.Close() }()
	n := mustNew(t, callbackOptions(s))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no poll")
	}
	sendCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := n.SendTest(sendCtx); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not join")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("request not cancelled")
	}
}

func TestCallbackOffsetRetriesOwnerFailure(t *testing.T) {
	var n *Notifier
	var calls atomic.Int32
	var handles atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "getWebhookInfo"):
			fmt.Fprint(w, `{"ok":true,"result":{"url":""}}`)
		case strings.HasSuffix(r.URL.Path, "getUpdates"):
			var req struct {
				Offset  int64    `json:"offset"`
				Allowed []string `json:"allowed_updates"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			call := calls.Add(1)
			want := int64(0)
			if call >= 3 {
				want = 6
			}
			if req.Offset != want || len(req.Allowed) != 1 || req.Allowed[0] != "callback_query" {
				t.Error("bad poll", req)
			}
			if call >= 3 {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"ok":false,"error_code":409}`)
				return
			}
			data, _ := json.Marshal([]callbackUpdate{callbackFixture(t, n)})
			fmt.Fprintf(w, `{"ok":true,"result":%s}`, data)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	defer s.Close()
	o := callbackOptions(s)
	o.HandleAction = func(context.Context, ActionClaims) (ActionResult, error) {
		if handles.Add(1) == 1 {
			return ActionResult{}, errors.New("transient")
		}
		return ActionResult{}, nil
	}
	n = mustNew(t, o)
	n.pollCallbacks(context.Background())
	if handles.Load() != 2 || calls.Load() != 3 {
		t.Fatal("retry did not preserve offset")
	}
}
func TestCallbackConfiguration(t *testing.T) {
	for _, id := range []string{"-123", "@username", "+123", "00123"} {
		o := Options{Token: syntheticToken, ChatID: id, Callbacks: true, HandleAction: func(context.Context, ActionClaims) (ActionResult, error) { return ActionResult{}, nil }}
		if _, err := New(o); err == nil {
			t.Fatal("accepted", id)
		}
	}
}
