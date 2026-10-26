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

## Hardware gate — partially verified on AX3000T

Executed via authorized SSH on the actual AX3000T/OpenWrt 24.10.2, with the test binary, state and logs confined to a private directory in `/tmp`:

- Verified uploaded binary SHA-256 and native ARM64 execution.
- Both real AP snapshots provide boolean `authorized`; 14 authorized Wi-Fi clients were silently imported with zero `new_device` logs.
- Gracefully stopped/restarted only the test daemon: all registry identities were retained, no rediscovery alerts.
- Terminated only its owned `ubus subscribe` child (not ubus/hostapd services): the daemon resubscribed without new-device logs.
- Captured real JSON-line notifications during the user's phone reconnect: 47 probes, two disassociations, one inactive-deauth, one auth, one assoc and one successful authorization; zero malformed lines.
- Matched the authorized phone to registry `disconnected` then `connected` history; registry remained at 14 devices with zero `new_device` logs. Probe/pre-authorization traffic did not add devices.
- With the phone offline, backed up the original registry and prepared a separate initialized test registry missing only that phone identity. On reconnect, the phone produced exactly one `new_device` log, became online, and remained at one log after another reconciliation. The original registry remained byte-identical to its backup.
- Stopped the test daemon gracefully, archived the private results locally, verified the archive contents, and removed the test directory from router `/tmp`. No permanent daemon/service was installed.
- Measured daemon RSS at approximately 4–6 MiB during these observations, with `GOMEMLIMIT=12MiB` and `GOGC=50`. This is a short-run observation, not a long-term memory bound.
- Read back unchanged hashes of network, wireless, firewall, DHCP and system configuration; WAN remained up. No network/Wi-Fi/firewall service was reloaded or restarted.

Access bootstrap used a user-approved SSH public key installed through authenticated LuCI. At the user's explicit request, that dedicated key was retained for future access; fresh key-only SSH authentication was verified after test cleanup. The private key remains outside the repository with mode 0600. Private MACs, raw logs, credentials and addresses are not included in this repository.

### Still open

1. Band roaming, actual hostapd/Wi-Fi reload and full AP removal are not hardware-tested. These disruptive operations are deliberately prohibited on the user's live Internet connection unless separately approved.
2. Durable `/etc` writes and long-run flash/RAM behavior remain untested; all daemon state writes so far are in RAM-backed `/tmp`.
3. Telegram delivery, protected LuCI integration, procd lifecycle and official `.ipk` installation remain future phases; no Telegram request, service installation or firmware change has been performed.

See README.md for manual commands. Local automated tests do not close the remaining hardware/product criteria.
