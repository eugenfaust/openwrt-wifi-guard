package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestCallbackFullTextAndKeyboard(t *testing.T) {
	for _, mode := range []string{"replace", "preserve", "remove"} {
		t.Run(mode, func(t *testing.T) {
			warning := strings.Repeat("Предупреждение: blocking may disconnect this device. ", 12)
			original := [][]InlineButton{{{Text: "Old", Data: "old"}}}
			keyboard := [][]InlineButton{{{Text: "Confirm", Data: "confirm"}, {Text: "Cancel", Data: "cancel"}}}
			expected := keyboard
			if mode == "preserve" {
				keyboard = nil
				expected = original
			}
			if mode == "remove" {
				keyboard = [][]InlineButton{}
				expected = keyboard
			}
			var edits, answers int
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Text      string       `json:"text"`
					ChatID    int64        `json:"chat_id"`
					MessageID int64        `json:"message_id"`
					Markup    inlineMarkup `json:"reply_markup"`
					ParseMode string       `json:"parse_mode"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "editMessageText"):
					edits++
					if !strings.Contains(req.Text, warning) || !strings.Contains(req.Text, "02:00:00:00:00:01") || req.ChatID != 12345 || req.MessageID != 9 || req.ParseMode != "" || !reflect.DeepEqual(req.Markup.Keyboard, expected) {
						t.Errorf("lost result, target, or keyboard: %+v", req)
					}
				case strings.HasSuffix(r.URL.Path, "answerCallbackQuery"):
					answers++
					if len(req.Text) > 200 {
						t.Error("oversized acknowledgement")
					}
				default:
					t.Error("unexpected method", r.URL.Path)
				}
				fmt.Fprint(w, `{"ok":true,"result":true}`)
			}))
			defer s.Close()
			o := callbackOptions(s)
			o.HandleAction = func(context.Context, ActionClaims) (ActionResult, error) {
				return ActionResult{Text: warning, Keyboard: keyboard}, nil
			}
			n := mustNew(t, o)
			u := callbackFixture(t, n)
			// Decode the original Telegram keyboard as it arrives with the message.
			raw, _ := json.Marshal(map[string]any{"reply_markup": inlineMarkup{Keyboard: original}})
			if err := json.Unmarshal(raw, u.Query.Message); err != nil {
				t.Fatal(err)
			}
			if !n.processCallback(context.Background(), u) || edits != 1 || answers != 1 {
				t.Fatalf("edits=%d answers=%d", edits, answers)
			}
		})
	}
}
