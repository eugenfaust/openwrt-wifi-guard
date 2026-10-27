# Telegram notifier

Standard-library-only, best-effort delivery; no network activity during `New`.

## Integration contract

- `New(Options) (*Notifier, error)` validates a bot token, numeric nonzero chat ID (signed int64), or `@username`. Zero-valued delivery settings select defaults: capacity 32, 3 attempts, 8-second per-attempt timeout, 1-second initial exponential backoff. Negative settings are rejected.
- `Run(context.Context)` starts one sequential worker. Run it in a goroutine; repeated calls return immediately. Cancel its context and wait for that goroutine on shutdown. It does not drain the queue on cancellation and cannot be restarted.
- `Enqueue(Message) bool` is concurrency-safe and nonblocking. False means full or stopped; the owner should track that drop if needed. A true result only means queued, not delivered. Enqueue before Run is supported. An enqueue concurrent with cancellation can still be accepted and then dropped.
- `SendTest(context.Context) error` synchronously sends a clearly labeled test without a fabricated identity. It shares the worker's serialization gate; its caller's context controls both gate waiting and delivery. It does not require Run, and has an independent lifetime from Run.
- `Format(Message) string` returns plaintext MAC, RFC3339 UTC timestamp, and interface. Test messages ignore all supplied device fields. Control characters in metadata are removed. No SSID, hostname, IP, vendor, or trust is invented.

Production origin is `https://api.telegram.org`. `Options.BaseURL` exists for local fake-server tests only; it additionally accepts loopback HTTP(S) origins. Do not expose it through UCI. `Client` is copied before installing a no-redirect policy, so the caller's client is not mutated. Injected clients/transports/loggers are trusted dependencies and must not themselves record secret request URLs.

Transport errors, HTTP 5xx and 429, and equivalent Telegram API errors are retried within the attempt budget. Other HTTP errors, malformed success JSON, missing `ok`, and missing/nonpositive `result.message_id` are terminal. Incomplete response-body transport reads are retryable. Response bodies are limited to 64 KiB; no response text or wrapped URL error is returned or logged. Retry delays, including Telegram `parameters.retry_after`, are capped at 30 seconds. Redirects are not followed. Requests use JSON with link previews disabled, without a parse mode.

## Limits

The queue is volatile: full queues, shutdown, process failure, and exhausted retries lose notifications. Timeout/retry ambiguity can produce duplicates if Telegram accepted a request but its response was lost; there is no exactly-once guarantee. `Run` and `SendTest` must receive appropriately scoped cancellation contexts. No real Telegram, credentials, or router are used by package tests.

Tests are in `notifier_test.go` and `lifecycle_test.go`; run `go test -race ./internal/telegram` from the module root.
