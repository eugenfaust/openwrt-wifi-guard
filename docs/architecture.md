# Architecture and phase boundaries

## Phase A data path

One Go process, standard library, no CGO or listening sockets:

`ubus list hostapd.*` → sorted AP objects → `ubus subscribe <argv...>` → bounded line scanner → single-owner registry → batched atomic JSON persistence.

A subscription is established before querying baseline snapshots to reduce startup gaps. The CLI has no explicit ready acknowledgement, so this is not a transactional barrier; periodic reconciliation is the recovery mechanism. `ubus call <object> get_clients` accepts only `authorized: true`. Each command has its own deadline. Events are bounded to 256 KiB per line; accepted events use a 128-item channel with backpressure. Probes never mutate state. Stderr from ubus is not forwarded into service logs. CLI errors, subscription EOF and discovery failures are observable without dumping raw input.

The initial `ubus` adapter intentionally uses the verified JSON-line `subscribe` interface, not `listen`. Upstream ubus CLI `print_event` formats each event with `blobmsg_format_json` and flushes stdout. Actual firmware framing must still be tested. AP object suffix is used as the snapshot interface name (matching the observed phy*-ap* layout); explicit event `ifname` is required.

## Lifecycle

- New state begins uninitialized. After nonempty AP discovery, `import` silently records the first snapshot attempt and drains the events already buffered during that attempt before closing a finite startup event window. Each initial AP awaiting its first successful snapshot stays in persisted `bootstrap_pending`; only that snapshot is suppressed, not subsequent live events. Failure of one AP cannot indefinitely silence other authorizations. This chooses silence for observations in the initial window because their pre-start/new identity cannot be reliably distinguished.
- `notify` initializes immediately, making first observations produce `new_device` log entries. Previously known MACs never produce another new-device entry.
- Snapshot positives across all successfully queried APs are applied before negatives. A failed AP snapshot does not mark its clients offline. Removed APs are retired only after a complete successful snapshot cycle.
- Best-effort disconnection only affects the last observed interface. A stale old-band disconnect does not take a client offline after a new-band authorization. Delayed events can still momentarily be wrong; authoritative snapshots repair them.
- Discovery/object changes cancel and reap the old child before starting another subscription. Accepted buffered events are drained before discarding the subscription, including on EOF and shutdown. A successful empty discovery marks all clients offline, unlike a failed discovery. Unexpected EOF retries at a fixed bounded interval; procd process-level backoff is deferred to packaging. Wi-Fi changes are rediscovered at the reconciliation interval. Every 5m (configurable), a successful reconciliation discovery also renews an unchanged-name subscription to recover silently stale object IDs; renewal is not a claim that the real firmware has been tested. No AP / failed bootstrap also retries at the retry interval.
- SIGINT/SIGTERM cancels child processes and writes dirty state. No detached child or shutdown notification delivery exists.

## Storage

Versioned JSON holds registry and bounded lifecycle history in one atomic replacement. Temporary files are created alongside the destination with 0600, fsynced, renamed and parent-directory fsynced. Newly created directory permissions are 0700; existing directory permissions are not altered, but its owner must match the process and group/other must not have write access. The state file must be a private regular file of the same owner; direct symlinks are rejected. Keep ancestor directories trusted as well. Failed loads stop startup rather than resetting deduplication identity. A matching 16 MiB read/write limit prevents saving a registry that cannot be reloaded. An oversized temporary file is discarded before rename, preserving the previous working registry. Known identities are never evicted automatically: capacity errors are explicit and need operator intervention; until resolved, new in-memory observations are not durable.

The main loop is the sole registry owner. There is no concurrent RPC reader or mutation. One daemon must own each state file (not enforced with a lock in Phase A). Critical identity/lifecycle updates flush periodically (30s default). Last-seen-only updates keep exact in-memory timestamps but checkpoint at 15m by default; lifecycle saves include them too. Graceful shutdown drains accepted events and flushes both classes. A power cut can lose pending observations or last-seen freshness; storage failure can extend this window. Exact-once logging across a power failure is not promised. Time is UTC, wall-clock sourced; reliable router time is an operational prerequisite for meaningful timestamps.

## Deferred interfaces

Telegram delivery will be asynchronous, bounded and separately tested against a fake server. Its credentials must be in restricted UCI, not this registry or read APIs. LuCI will use native authentication and a restricted rpcd executable bridge with explicit ACL tests, not an unauthenticated HTTP service. No placeholder RPC is exposed in Phase A. Trust `blocked` is reserved metadata, never a permanent blocking policy. Router deployment, procd and official SDK packaging require later verified phases.

## Intentional deviations

Phase A emits structured logs instead of Telegram alerts, takes CLI flags instead of UCI, and has no service/package installer. This follows the initial phase gate; it is not a claim of finished end-to-end product delivery. SSID/band/IP/hostname enrichment, repeated-appearance alerts, trust mutation and UI are deferred. Hardware validation is required before those phases.
