# Telegram notifier

Standard-library-only, best-effort delivery; no network activity during `New`.

## Integration contract

- `New(Options) (*Notifier, error)` validates a bot token, numeric nonzero chat ID (signed int64), or `@username`. Zero-valued delivery settings select defaults: capacity 32, 3 attempts, 8-second per-attempt timeout, 1-second initial exponential backoff. Negative settings are rejected.
- `Run(context.Context)` starts one sequential worker. Run it in a goroutine; repeated calls return immediately. Cancel its context and wait for that goroutine on shutdown. It does not drain the queue on cancellation and cannot be restarted.
- `Enqueue(Message) bool` is concurrency-safe and nonblocking. False means full or stopped; the owner should track that drop if needed. A true result only means queued, not delivered. Enqueue before Run is supported. An enqueue concurrent with cancellation can still be accepted and then dropped.
- `SendTest(context.Context) error` synchronously sends a clearly labeled test without a fabricated identity. It shares the worker's serialization gate; its caller's context controls both gate waiting and delivery. It does not require Run, and has an independent lifetime from Run.
- `Format(Message) string` returns plaintext MAC, RFC3339 UTC timestamp, and interface. Test messages ignore all supplied device fields. Control characters in metadata are removed. No SSID, hostname, IP, vendor, or trust is invented.

Production origin is `https://api.telegram.org`. `Options.BaseURL` exists for local fake-server tests only; it additionally accepts loopback HTTP(S) origins. Do not expose it through UCI. `Client` is copied before installing a no-redirect policy, so the caller's client is not mutated. Injected clients/transports/loggers are trusted dependencies and must not themselves record secret request URLs.

Transport errors, HTTP 5xx and 429, and equivalent Telegram API errors are retried within the attempt budget. Other HTTP errors, malformed success JSON, missing `ok`, and missing/nonpositive `result.message_id` are terminal. Incomplete response-body transport reads are retryable. Response bodies are limited to 64 KiB; no response text or wrapped URL error is returned or logged. Local exponential backoff is capped at 30 seconds. Telegram `parameters.retry_after` is preserved as a shared outbound cooldown (saturating at the maximum duration to avoid overflow), including across separate send calls. Redirects are not followed. Requests use JSON with link previews disabled, without a parse mode.

## Limits

The queue is volatile: full queues, shutdown, process failure, and exhausted retries lose notifications. Timeout/retry ambiguity can produce duplicates if Telegram accepted a request but its response was lost; there is no exactly-once guarantee. `Run` and `SendTest` must receive appropriately scoped cancellation contexts. No real Telegram, credentials, or router are used by package tests.

Tests use loopback fixtures and synthetic credentials; run `go test -race ./internal/telegram -timeout=60s` from the module root.

## Opt-in private callbacks

`Options.Callbacks` requires a canonical positive numeric private-chat ID and a
`HandleAction` handler. Both sender and message chat must match that ID. Bots,
groups, inline or inaccessible messages, invalid signatures and expired actions
are silently rejected. Signed tokens are canonical, chat-bound and tied to the
bot credential generation, with a seven-day default validity and two-minute
future clock tolerance. Nonces are nonzero uint64 values; `ActionKeyboard`
preserves the supplied nonce and issue time.

Signature verification is not replay protection. The owner must durably validate
the current device/review nonce, authorize each transition and enforce idempotency.
Transient handler failures retain the polling offset; committed actions are not
replayed merely because Telegram UI writes fail. Offsets are in memory and restart
replay is possible.

Nonempty `ActionResult.Text` edits the original message via `editMessageText`,
including the full plaintext result and signed device MAC. The short callback toast
is only an acknowledgement. A nil keyboard preserves the original callback
keyboard; a non-nil empty keyboard removes it. Empty text changes only the keyboard
when supplied. UI writes are best effort, not durably retried. Handlers must keep
text within Telegram's single-message limit including the MAC header; automatic
splitting is not implemented.

There is no public listener. Polling starts only after a successful empty-webhook
check; it never deletes a webhook and stops on a polling conflict. Configure only
one poller per bot: conflict detection is not a distributed lock. Pending long polls
do not hold the outbound delivery gate; callback writes and notifications share
the cooldown. A canceled Run joins its callback worker.

The long-poll cancellation fixture drains the POST body before waiting for request
cancellation, and has an explicit teardown release. Otherwise net/http can leave
the handler waiting indefinitely and `httptest.Server.Close` deadlocks, even after
the client worker has exited.
