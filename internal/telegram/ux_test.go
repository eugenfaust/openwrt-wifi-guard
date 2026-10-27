package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNotificationReadableNameAndLocalClock(t *testing.T) {
	t.Setenv("TZ", "MSK-3")
	m := Message{MAC: "02:00:00:00:00:01", Name: "Kitchen sensor", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Interface: "phy1-ap0"}
	text := formatForSend(context.Background(), m)
	for _, want := range []string{"Kitchen sensor", "02:00:00:00:00:01", "2026-01-02 06:04:05", "+03:00", "phy1-ap0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "2026-01-02T03:04:05Z") {
		t.Fatal("UTC time instead of local")
	}
	m.Name = "  <script>\nInjected\rName  "
	text = formatForSend(context.Background(), m)
	if strings.Contains(text, "\nInjected") || strings.Contains(text, "\r") || strings.Contains(text, "<script>") {
		t.Fatalf("unsafe name: %q", text)
	}
	m.Name = ""
	if strings.Contains(formatForSend(context.Background(), m), "Устройство: ") {
		t.Fatal("blank name displayed")
	}
}

func TestRussianCallbackFeedbackAndCharacterLimit(t *testing.T) {
	for _, tc := range []struct {
		name, result, want string
		fail               bool
	}{
		{"error", "", "Не удалось выполнить действие. Попробуйте ещё раз.", true},
		{"russian_200_characters", strings.Repeat("я", 200), strings.Repeat("я", 200), false},
		{"too_long", strings.Repeat("я", 201), "Действие обработано.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var answers int
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
					var p struct {
						Text string `json:"text"`
					}
					if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
						t.Error(err)
					}
					if p.Text != tc.want {
						t.Errorf("feedback = %q; want %q", p.Text, tc.want)
					}
					answers++
				}
				fmt.Fprint(w, `{"ok":true,"result":true}`)
			}))
			defer s.Close()
			o := callbackOptions(s)
			o.HandleAction = func(context.Context, ActionClaims) (ActionResult, error) {
				if tc.fail {
					return ActionResult{}, fmt.Errorf("owner unavailable")
				}
				return ActionResult{Text: tc.result}, nil
			}
			n := mustNew(t, o)
			if got := n.processCallback(context.Background(), callbackFixture(t, n)); got == tc.fail {
				t.Fatal("wrong retry disposition")
			}
			if answers != 1 {
				t.Fatalf("answers = %d", answers)
			}
		})
	}
}

func TestRussianInitialKeyboardRetainsSignedActions(t *testing.T) {
	n := mustNew(t, Options{Token: syntheticToken, ChatID: "12345"})
	claims := ActionClaims{MAC: "02:00:00:00:00:01", Nonce: 1, Issued: time.Now()}
	keys, err := n.ActionKeyboard(claims)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || len(keys[0]) != 2 {
		t.Fatalf("wrong keyboard: %+v", keys)
	}
	for i, label := range []string{"Доверять", "Заблокировать"} {
		if keys[0][i].Text != label {
			t.Errorf("label: %q", keys[0][i].Text)
		}
		got, err := n.codec.Decode(keys[0][i].Data, 12345, time.Now())
		want := []Action{ActionTrust, ActionBlockPrompt}[i]
		if err != nil || got.Action != want || got.MAC != claims.MAC || got.Nonce != claims.Nonce {
			t.Fatalf("changed claim: %+v %v", got, err)
		}
	}
}

func TestCallbackEditsSameMessageWithNameAndStatus(t *testing.T) {
	var methods []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		methods = append(methods, method)
		var p struct {
			Text      string       `json:"text"`
			ChatID    int64        `json:"chat_id"`
			MessageID int64        `json:"message_id"`
			Markup    inlineMarkup `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if method == "editMessageText" && (p.ChatID != 12345 || p.MessageID != 9 || !strings.Contains(p.Text, "Kitchen sensor") || !strings.Contains(p.Text, "02:00:00:00:00:01") || !strings.Contains(p.Text, "Ожидает подтверждения")) {
			t.Errorf("wrong edit: %+v", p)
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	defer s.Close()
	o := callbackOptions(s)
	o.HandleAction = func(context.Context, ActionClaims) (ActionResult, error) {
		return ActionResult{Text: "Ожидает подтверждения", Name: "Kitchen sensor", Keyboard: [][]InlineButton{{{Text: "Подтвердить", Data: "signed"}}}}, nil
	}
	n := mustNew(t, o)
	if !n.processCallback(context.Background(), callbackFixture(t, n)) {
		t.Fatal("callback failed")
	}
	if fmt.Sprint(methods) != "[answerCallbackQuery editMessageText]" {
		t.Fatalf("unexpected writes: %v", methods)
	}
}
