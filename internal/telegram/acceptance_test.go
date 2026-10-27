package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAcceptanceIsObservableWithoutCredentials(t *testing.T) {
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer server.Close()
	options := options(server)
	options.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	notifier := mustNew(t, options)
	if err := notifier.SendTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Msg       string `json:"msg"`
		MessageID int64  `json:"message_id"`
		Test      bool   `json:"test"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
		t.Fatal("acceptance not observable", err)
	}
	if entry.Msg != "telegram_notification_accepted" || entry.MessageID != 42 || !entry.Test {
		t.Fatal("missing acceptance metadata")
	}
	assertSecret(t, logs.String())
}
