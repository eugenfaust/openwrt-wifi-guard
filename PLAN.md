# Implementation plan and status

## Phase A — daemon: implemented and checked

Dynamic AP subscriptions, authorization-only registry, silent bootstrap, per-AP reconciliation, persistence, history, deduplication, recovery and shutdown. Local race/smoke tests and ARM64 build passed; non-disruptive AX3000T phone/restart/subscription tests passed.

## Phase B — Telegram/UCI: implemented; real test receipt verified

- Bounded asynchronous notifier, official HTTPS, retries/backoff/timeouts and explicit test notification.
- Private UCI staging/readback, token-free read APIs/logs/argv and empty-libuci-option compatibility.
- Fake Bot API tests and real AX3000T UCI tests passed; minimum Go1.23 tests passed.
- An existing securely provisioned router token matched the authorized chat. The native test RPC returned success and the user explicitly confirmed receiving the labeled test message. No token was extracted from the browser vault or printed. Automatic delivery was subsequently enabled and read back; an isolated-registry reconciliation produced one real notification whose receipt the user also confirmed.

## Phase C — LuCI/RPC: implemented and checked

Native devices/history/settings view, rename/trust, settings save/test controls, concrete restricted rpcd executable + private Unix bridge. Real-browser rename/trust/save passed; no-session requests denied; transient read-only session could read but not mutate/test or use generic UCI. Original device metadata was restored after tests. rpcd signatures/session metadata and HTML boolean-attribute regressions are covered.

## Phase D — IPK/procd: r2 installed and checked

Separate real official-SDK packages for ARM64 daemon and architecture-independent LuCI. Native installation, enabled/running procd service, root:0600 state/config/socket and reinstall preservation of config/identity/first_seen/name/trust passed. CI definition includes tests/SDK/artifact upload but no remote CI run is claimed. Release artifacts are under build/packages (gitignored).

## Remaining acceptance gates / follow-on

- Real test-message and isolated automatic detection-to-Telegram receipt were both confirmed. Automatic notifications are enabled on the installed service.
- Disruptive hostapd/Wi-Fi restart, AP removal, band roaming and router reboot only in a separately approved window; not required just to add alerts/UI.
- Long-run flash/RAM/network-failure durability monitoring. Current outbox is bounded in-memory best-effort, not durable exactly-once delivery.
- Optional callbacks, repeat-appearance alerts, metadata enrichment, i18n and additional target architectures later; no automatic/permanent blocking.

## Hardening integrated in r2

Exclusive registry writer lock; nonblocking serialized UCI worker; independent post-publication recovery; no-op queue preservation; credential-generation cancellation and shared Telegram tests; server retry_after cooldown; RPC disconnect cancellation; fail-safe LuCI settings recovery. Local regressions, ARM64/minimum-Go builds, official SDK artifacts and native upgrade/no-op/lock/UI checks passed.

See docs/verification.md for executed checks and docs/security.md for explicit limits. Do not declare the full product acceptance closed while the above live gates remain open.
