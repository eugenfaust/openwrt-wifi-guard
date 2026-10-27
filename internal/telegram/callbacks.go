package telegram

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// InlineButton is a Telegram callback button, not an arbitrary URL button.
type InlineButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}
type ActionResult struct {
	Text string
	// Nil leaves the keyboard unchanged; an empty non-nil slice removes it.
	Keyboard [][]InlineButton
}
type inlineMarkup struct {
	Keyboard [][]InlineButton `json:"inline_keyboard"`
}

func actionCodec(token string) *ActionCodec {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("wifi-guard/telegram/action-key/v1"))
	codec, _ := NewActionCodec(mac.Sum(nil), 7*24*time.Hour)
	return codec
}

// EncodeAction signs a claim for this credential generation and numeric chat.
// The owner supplies persistent nonces and enforces replay/idempotency policy.
func (n *Notifier) EncodeAction(c ActionClaims) (string, error) {
	if n.callbackChat <= 0 {
		return "", ErrInvalidAction
	}
	return n.codec.Encode(c, n.callbackChat)
}

// ActionKeyboard creates the initial review buttons without changing the nonce
// or issue time. Confirmation/cancel keyboards can be built with EncodeAction.
func (n *Notifier) ActionKeyboard(c ActionClaims) ([][]InlineButton, error) {
	row := make([]InlineButton, 0, 2)
	for _, item := range []struct {
		action Action
		text   string
	}{{ActionTrust, "Trust"}, {ActionBlockPrompt, "Block…"}} {
		c.Action = item.action
		data, err := n.EncodeAction(c)
		if err != nil {
			return nil, err
		}
		row = append(row, InlineButton{Text: item.text, Data: data})
	}
	return [][]InlineButton{row}, nil
}

type botReply struct {
	OK         bool            `json:"ok"`
	Result     json.RawMessage `json:"result"`
	ErrorCode  int             `json:"error_code"`
	Parameters struct {
		RetryAfter int64 `json:"retry_after"`
	} `json:"parameters"`
}

// callbackRequest deliberately never exposes response text or URL-bearing errors.
// Polling uses it outside gate: a pending long poll cannot block delivery.
func (n *Notifier) callbackRequest(ctx context.Context, method string, payload any) (botReply, int, error) {
	var reply botReply
	body, err := json.Marshal(payload)
	if err != nil {
		return reply, 0, errors.New("Telegram request encoding failed")
	}
	ctx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.baseURL+"/bot"+n.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return reply, 0, errors.New("Telegram request creation failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return reply, 0, errors.New("Telegram transport failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
	if err != nil || len(data) > 256*1024 || json.Unmarshal(data, &reply) != nil {
		return botReply{}, resp.StatusCode, errors.New("invalid Telegram response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !reply.OK {
		return reply, resp.StatusCode, errors.New("Telegram request rejected")
	}
	return reply, resp.StatusCode, nil
}

func (n *Notifier) callbackGate(ctx context.Context) error {
	select {
	case n.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	for remaining := n.cooldown.Sub(n.now()); remaining > 0; remaining = n.cooldown.Sub(n.now()) {
		if err := n.sleep(ctx, remaining); err != nil {
			<-n.gate
			return err
		}
	}
	return nil
}
func (n *Notifier) callbackCooldown(reply botReply) {
	seconds := reply.Parameters.RetryAfter
	if seconds <= 0 {
		return
	}
	const maxDuration = time.Duration(1<<63 - 1)
	after := maxDuration
	if seconds <= int64(maxDuration/time.Second) {
		after = time.Duration(seconds) * time.Second
	}
	until := n.now().Add(after)
	if until.After(n.cooldown) {
		n.cooldown = until
	}
}
func (n *Notifier) callbackWrite(ctx context.Context, method string, payload any) {
	if n.callbackGate(ctx) != nil {
		return
	}
	defer func() { <-n.gate }()
	reply, _, _ := n.callbackRequest(ctx, method, payload)
	n.callbackCooldown(reply)
}

type callbackUpdate struct {
	ID    int64 `json:"update_id"`
	Query *struct {
		ID              string `json:"id"`
		Data            string `json:"data"`
		InlineMessageID string `json:"inline_message_id"`
		From            struct {
			ID  int64 `json:"id"`
			Bot bool  `json:"is_bot"`
		} `json:"from"`
		Message *struct {
			ID     int64         `json:"message_id"`
			Date   int64         `json:"date"`
			Markup *inlineMarkup `json:"reply_markup"`
			Chat   struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// processCallback returns false only for transient owner failures. Authentication
// failures are terminal, silent rejects, never answered to an attacker.
func (n *Notifier) processCallback(ctx context.Context, u callbackUpdate) bool {
	q := u.Query
	if !n.callbacks || n.callbackChat <= 0 || n.handleAction == nil || q == nil || q.ID == "" || q.InlineMessageID != "" || q.From.Bot || q.From.ID != n.callbackChat || q.Message == nil || q.Message.ID <= 0 || q.Message.Date <= 0 || q.Message.Chat.ID != n.callbackChat || q.Message.Chat.Type != "private" {
		return true
	}
	claims, err := n.codec.Decode(q.Data, n.callbackChat, n.now())
	if err != nil {
		return true
	}
	result, err := n.handleAction(ctx, claims)
	if err != nil {
		n.callbackWrite(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID, "text": "Action could not be completed. Please try again."})
		return false
	}
	// Owner commit, not Telegram acknowledgement, is the durable boundary. UI
	// failures must not reapply a committed action; owner also enforces nonces.
	text := result.Text
	if len(text) > 200 {
		text = "Action processed."
	}
	n.callbackWrite(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID, "text": text})
	if result.Text != "" {
		// A toast is transient and limited to 200 characters. Keep the full
		// warning/confirmation in the message, bound visibly to this device.
		markup := q.Message.Markup
		if result.Keyboard != nil {
			markup = &inlineMarkup{Keyboard: result.Keyboard}
		}
		payload := map[string]any{
			"chat_id": n.callbackChat, "message_id": q.Message.ID,
			"text":                 fmt.Sprintf("Wi-Fi Guard\nMAC: %s\n\n%s", claims.MAC, result.Text),
			"link_preview_options": map[string]bool{"is_disabled": true},
		}
		if markup != nil {
			payload["reply_markup"] = markup
		}
		n.callbackWrite(ctx, "editMessageText", payload)
	} else if result.Keyboard != nil {
		n.callbackWrite(ctx, "editMessageReplyMarkup", map[string]any{"chat_id": n.callbackChat, "message_id": q.Message.ID, "reply_markup": inlineMarkup{Keyboard: result.Keyboard}})
	}
	return true
}

func (n *Notifier) pollCallbacks(ctx context.Context) {
	// Do not remove an operator's webhook or fight another polling consumer.
	reply, _, err := n.callbackRequest(ctx, "getWebhookInfo", struct{}{})
	select {
	case n.gate <- struct{}{}:
		n.callbackCooldown(reply)
		<-n.gate
	case <-ctx.Done():
		return
	}
	var webhook struct {
		URL *string `json:"url"`
	}
	if err != nil || json.Unmarshal(reply.Result, &webhook) != nil || webhook.URL == nil || *webhook.URL != "" {
		n.logger.Warn("telegram_callbacks_stopped", "reason", "webhook_configured_or_probe_failed")
		return
	}
	var offset int64
	for ctx.Err() == nil {
		if n.callbackGate(ctx) != nil {
			return
		}
		<-n.gate
		seconds := int64(n.timeout/time.Second) - 1
		if seconds < 0 {
			seconds = 0
		}
		if seconds > 25 {
			seconds = 25
		}
		reply, status, err := n.callbackRequest(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": seconds, "limit": 100, "allowed_updates": []string{"callback_query"}})
		// Briefly publish retry_after under the same gate used by all writes.
		select {
		case n.gate <- struct{}{}:
			n.callbackCooldown(reply)
			<-n.gate
		case <-ctx.Done():
			return
		}
		if status == 409 || reply.ErrorCode == 409 {
			n.logger.Warn("telegram_callbacks_stopped", "reason", "polling_conflict")
			return
		}
		if status == 401 || status == 403 || reply.ErrorCode == 401 || reply.ErrorCode == 403 {
			n.logger.Warn("telegram_callbacks_stopped", "reason", "authorization_rejected")
			return
		}
		if err != nil {
			if n.sleep(ctx, boundedDelay(n.backoff)) != nil {
				return
			}
			continue
		}
		var updates []callbackUpdate
		if json.Unmarshal(reply.Result, &updates) != nil {
			if n.sleep(ctx, boundedDelay(n.backoff)) != nil {
				return
			}
			continue
		}
		failed := false
		for _, u := range updates {
			if u.ID < offset || u.ID < 0 || u.ID == int64(1<<63-1) {
				continue
			}
			if !n.processCallback(ctx, u) {
				failed = true
				break
			}
			offset = u.ID + 1
		}
		// Avoid busy spinning on fake/abnormal servers that return immediately.
		if failed || len(updates) == 0 {
			if n.sleep(ctx, boundedDelay(n.backoff)) != nil {
				return
			}
		}
	}
}
