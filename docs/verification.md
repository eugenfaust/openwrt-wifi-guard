# Phase A verification

## Verified on development host

Toolchain: Go 1.27.3 linux/amd64, downloaded from go.dev with the published SHA-256 checked before extraction. Toolchain is outside this repository at `/home/raven/.local/share/wifi-guard-toolchain/go/`. The declared minimum Go 1.23.0 was separately exercised with `GOTOOLCHAIN=go1.23.0 go test ./...`.

Reproduce on this host:

```sh
export PATH=/home/raven/.local/share/wifi-guard-toolchain/go/bin:$PATH
make check smoke arm64
go test -race -count=20 ./internal/monitor
GOTOOLCHAIN=go1.23.0 go test ./...
file build/wifi-guard build/wifi-guard-linux-arm64
```

Checks exercise:

- MAC validation and canonicalization with synthetic locally administered fixtures.
- Actual observed authorization JSON envelope, malformed JSON, irrelevant event rejection.
- Snapshot `authorized: true` filtering, missing/invalid snapshot failure.
- Silent bootstrap and explicit notify policy, per-AP pending snapshot retry without silencing live authorizations, and queued startup-event import.
- Accepted-event queue drain on subscription EOF/shutdown; last-seen-only updates do not force critical writes.
- Unsafe directory/symlink rejection; oversized saves preserve the previous working registry.
- New-MAC deduplication, reconnects, roaming and old-interface disconnect rejection.
- Missed events recovered from snapshots; failed AP snapshots do not force offline.
- Subprocess discovery, subscription EOF, idle cancellation and command timeouts.
- Subscription recovery, same-name periodic renewal and AP object changes; successful empty discovery marks clients offline.
- History bounds, atomic persistence round trip, permissions and corrupt-state refusal.
- Storage failure logging/retry and nonzero exit on failed shutdown flush.
- The compiled CLI against an executable fake ubus: silent existing-client import, no probes/auth/assoc, exactly one new-device log, subprocess reconnection, restart dedup, dump and SIGTERM.

`file` identifies the target binary as statically linked ARM aarch64 ELF. The host binary is static x86-64 ELF. Neither result claims execution on the router. GitHub Actions is configured, but no remote CI run is claimed.

## Required hardware gate (not performed)

Follow the SSH commands in README.md using the actual authorized router address. Confirm:

1. OpenWrt 24.10.2 accepts the static ARM64 binary and real `get_clients` matches the parser.
2. Subscription event JSON is one object per line and remains alive when idle.
3. Phone probes never add/alert; a genuinely new authorization creates one log promptly.
4. Reconnect/band roaming does not rediscover a known device.
5. Graceful daemon restart preserves identities; hostapd restart/Wi-Fi reload recovers subscriptions.
6. Radio/AP disable updates online state; no Ethernet client is imported.
7. Durable `/etc` writes work on the real flash filesystem at acceptable resource usage.

No router login, file upload, firmware/config change, Telegram request, LuCI installation, procd enable or `.ipk` installation has been performed. Full-product acceptance (Telegram/LuCI/packaging) remains open.
