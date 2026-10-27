// Package telegram provides bounded, best-effort Telegram notifications.
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
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

// Options configures delivery. Zero numeric values select defaults.
// BaseURL is a test hook: only the production origin or loopback HTTP(S)
// servers are accepted. It must not be exposed as a user/UCI setting.
// Custom Clients/Transports are trusted dependencies and must not log URLs.
type Options struct {
	Token, ChatID         string
	BaseURL               string
	Client                *http.Client
	Timeout, Backoff      time.Duration
	MaxAttempts, Capacity int
	Logger                *slog.Logger
	Callbacks             bool
	HandleAction          func(context.Context, ActionClaims) (ActionResult, error)
}

// Message contains only reliable device metadata. Test ignores device fields.
type Message struct {
	MAC       string
	Name      string
	At        time.Time
	Interface string
	Test      bool
	Keyboard  [][]InlineButton
}

// Notifier owns a bounded in-memory queue. Failed or shutdown-pending messages
// are not persisted. Run may be called once; Enqueue is safe concurrently.
type Notifier struct {
	token, chatID, baseURL string
	client                 *http.Client
	timeout, backoff       time.Duration
	attempts               int
	logger                 *slog.Logger
	queue                  chan Message
	gate                   chan struct{}
	callbacks              bool
	handleAction           func(context.Context, ActionClaims) (ActionResult, error)
	codec                  *ActionCodec
	callbackChat           int64
	started, stopped       atomic.Bool
	// cooldown is protected by gate, including across separate send calls.
	cooldown time.Time
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{20,}$`)
var usernamePattern = regexp.MustCompile(`^@[A-Za-z][A-Za-z0-9_]{4,31}$`)

// New validates configuration without making network requests.
func New(o Options) (*Notifier, error) {
	if !tokenPattern.MatchString(o.Token) {
		return nil, errors.New("invalid Telegram token")
	}
	id, err := strconv.ParseInt(o.ChatID, 10, 64)
	if (err != nil || id == 0 || strings.HasPrefix(o.ChatID, "+")) && !usernamePattern.MatchString(o.ChatID) {
		return nil, errors.New("invalid Telegram chat ID")
	}
	if o.Callbacks && (err != nil || id <= 0 || strconv.FormatInt(id, 10) != o.ChatID || o.HandleAction == nil) {
		return nil, errors.New("Telegram callbacks require a positive numeric private chat and handler")
	}
	if o.Timeout < 0 || o.Backoff < 0 || o.MaxAttempts < 0 || o.Capacity < 0 {
		return nil, errors.New("invalid Telegram delivery limits")
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://api.telegram.org"
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !validOrigin(u) {
		return nil, errors.New("invalid Telegram endpoint")
	}
	if o.Timeout == 0 {
		o.Timeout = 8 * time.Second
	}
	if o.Backoff == 0 {
		o.Backoff = time.Second
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 3
	}
	if o.Capacity == 0 {
		o.Capacity = 32
	}
	client := http.Client{}
	if o.Client != nil {
		client = *o.Client
	}
	// Never follow even same-origin redirects: the bot token is part of the URL.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Notifier{callbacks: o.Callbacks, handleAction: o.HandleAction, codec: actionCodec(o.Token), callbackChat: id, token: o.Token, chatID: o.ChatID, baseURL: strings.TrimSuffix(o.BaseURL, "/"), client: &client, timeout: o.Timeout, backoff: o.Backoff, attempts: o.MaxAttempts, logger: o.Logger, queue: make(chan Message, o.Capacity), gate: make(chan struct{}, 1), now: time.Now, sleep: sleepContext}, nil
}
func validOrigin(u *url.URL) bool {
	if u.Scheme == "https" && u.Host == "api.telegram.org" {
		return true
	}
	return (u.Scheme == "http" || u.Scheme == "https") && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost") && u.Host != ""
}

// safeLine keeps untrusted labels on a single plaintext line.
func safeLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '<' || r == '>' {
			return -1
		}
		return r
	}, s))
}

// Format produces plaintext with an explicit UTC timestamp and no I/O.
// Delivery uses formatForSend to resolve the router's local timezone off-owner.
func Format(m Message) string {
	return formatAt(m, m.At.UTC(), "UTC; местное время недоступно")
}

func formatAt(m Message, at time.Time, zone string) string {
	if m.Test {
		return "Wi-Fi Guard — тестовое уведомление\nПроверка доставки уведомлений в Telegram."
	}
	name := ""
	if label := safeLine(m.Name); label != "" {
		name = "Устройство: " + label + "\n"
	}
	return fmt.Sprintf("Новое устройство Wi-Fi\n%sMAC: %s\nВремя (%s): %s\nИнтерфейс: %s", name, safeLine(m.MAC), zone, at.Format("2006-01-02 15:04:05 -07:00"), safeLine(m.Interface))
}

// Enqueue never waits for network I/O or queue space. False means dropped.
// The queue is never closed, avoiding send/close races during shutdown.
func (n *Notifier) Enqueue(m Message) bool {
	if n.stopped.Load() {
		return false
	}
	select {
	case n.queue <- m:
		return true
	default:
		return false
	}
}

// Run processes queued messages sequentially until cancellation. Shutdown drops
// pending work rather than delaying the owner's shutdown for network retries.
func (n *Notifier) Run(ctx context.Context) {
	if !n.started.CompareAndSwap(false, true) {
		return
	}
	defer n.stopped.Store(true)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if n.callbacks {
		done := make(chan struct{})
		go func() { defer close(done); n.pollCallbacks(ctx) }()
		defer func() { cancel(); <-done }()
	}
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case m := <-n.queue:
			if err := n.send(ctx, m); err != nil && ctx.Err() == nil {
				n.logger.Warn("Telegram notification failed", "error", err)
			}
		}
	}
}

// SendTest synchronously sends an explicitly labeled test, sharing the worker's
// serialization gate. It honors ctx even while waiting for another request.
func (n *Notifier) SendTest(ctx context.Context) error { return n.send(ctx, Message{Test: true}) }

const maxDelay = 30 * time.Second

func boundedDelay(d time.Duration) time.Duration {
	if d > maxDelay {
		return maxDelay
	}
	return d
}
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (n *Notifier) send(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case n.gate <- struct{}{}:
		defer func() { <-n.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	delay := boundedDelay(n.backoff)
	for attempt := 0; attempt < n.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for remaining := n.cooldown.Sub(n.now()); remaining > 0; remaining = n.cooldown.Sub(n.now()) {
			if err := n.sleep(ctx, remaining); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		retry, after, err := n.attempt(ctx, m)
		// Retain server advice even on the final attempt or cancellation:
		// the next queued notification/test shares this deadline.
		if after > 0 {
			until := n.now().Add(after)
			if until.After(n.cooldown) {
				n.cooldown = until
			}
		}
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retry || attempt == n.attempts-1 {
			return err
		}
		wait := delay
		if remaining := n.cooldown.Sub(n.now()); remaining > wait {
			wait = remaining
		}
		if err := n.sleep(ctx, wait); err != nil {
			return err
		}
		if delay < maxDelay/2 {
			delay *= 2
		} else {
			delay = maxDelay
		}
	}
	return errors.New("Telegram delivery failed")
}

func (n *Notifier) attempt(ctx context.Context, m Message) (bool, time.Duration, error) {
	payload := struct {
		ChatID  string        `json:"chat_id"`
		Text    string        `json:"text"`
		Markup  *inlineMarkup `json:"reply_markup,omitempty"`
		Preview struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	}{ChatID: n.chatID, Text: formatForSend(ctx, m)}
	payload.Preview.Disabled = true
	if m.Keyboard != nil && !m.Test {
		payload.Markup = &inlineMarkup{Keyboard: m.Keyboard}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, 0, errors.New("Telegram request encoding failed")
	}
	requestCtx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, n.baseURL+"/bot"+n.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return false, 0, errors.New("Telegram request creation failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		// In particular, never return/wrap url.Error: it contains the secret URL.
		return true, 0, errors.New("Telegram transport failed")
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 65537))
	var reply struct {
		OK     *bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
		ErrorCode  int `json:"error_code"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	valid := readErr == nil && len(data) <= 65536 && json.Unmarshal(data, &reply) == nil
	after := time.Duration(0)
	if valid && reply.Parameters.RetryAfter > 0 {
		seconds := reply.Parameters.RetryAfter
		// Saturate before multiplying: even MaxInt64 seconds must never
		// wrap into a negative or short delay.
		const maxDuration = time.Duration(1<<63 - 1)
		if seconds > int64(maxDuration/time.Second) {
			after = maxDuration
		} else {
			after = time.Duration(seconds) * time.Second
		}
	}
	if resp.StatusCode == 429 || resp.StatusCode >= 500 && resp.StatusCode <= 599 {
		return true, after, errors.New("Telegram service temporarily unavailable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, after, errors.New("Telegram request rejected")
	}
	if readErr != nil {
		return true, 0, errors.New("Telegram response transport failed")
	}
	if !valid || reply.OK == nil {
		return false, after, errors.New("invalid Telegram response")
	}
	if !*reply.OK {
		retry := reply.ErrorCode == 429 || reply.ErrorCode >= 500 && reply.ErrorCode <= 599
		return retry, after, errors.New("Telegram API rejected request")
	}
	if reply.Result.MessageID <= 0 {
		return false, after, errors.New("invalid Telegram success response")
	}
	n.logger.Info("telegram_notification_accepted", "message_id", reply.Result.MessageID, "test", m.Test)
	return false, after, nil
}
