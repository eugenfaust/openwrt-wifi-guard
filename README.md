# OpenWrt WiFi Guard

A lightweight, router-native Wi-Fi client monitor written in Go. **Phase A is implemented:** it discovers `hostapd.*` objects, subscribes to successful authorizations, maintains a persistent registry and emits structured `new_device` log entries. It does not monitor Ethernet clients.

**Not implemented yet:** Telegram delivery, LuCI, UCI configuration, procd installation and `.ipk` packages. See [PLAN.md](PLAN.md). No network listener is opened and no client is blocked.

[Русский](README.ru.md)

## Build and test

Go 1.23+ and Make; no third-party Go modules or CGO. Linux/macOS development, Linux router runtime. Race tests require a host compiler; the daemon does not.

```sh
make check
make build arm64
make smoke # Python 3 on development host only; uses a fake ubus, never a router
```

Outputs: `build/wifi-guard` (host) and `build/wifi-guard-linux-arm64` (static Linux ARM64). Cross-compilation is not proof of hardware compatibility. Target: AX3000T, OpenWrt 24.10.2, `aarch64_cortex-a53`; package builds will use the official OpenWrt SDK in Phase D.

## Manual router validation (no installation)

Use your actual SSH destination; do not assume a default LAN address. These commands require already-authorized SSH access. `scp -O` uses legacy SCP for OpenWrt systems without SFTP; omit `-O` if your host does not support it and SFTP works.

```sh
export ROUTER='root@YOUR_ROUTER_ADDRESS'
ssh "$ROUTER" 'ubus list "hostapd.*"'
# Inspect get_clients on an object printed above:
ssh "$ROUTER" 'ubus call hostapd.YOUR_AP_OBJECT get_clients'
scp -O build/wifi-guard-linux-arm64 "$ROUTER":/tmp/wifi-guard
ssh -t "$ROUTER" 'chmod 700 /tmp/wifi-guard; /tmp/wifi-guard -state /tmp/wifi-guard-test/state.json -reconcile 60s -flush 10s'
```

`/tmp` state is intentionally disposable: it does not survive router reboot. Ctrl-C flushes state and terminates/reaps the subscription. Do not run two instances against the same state file. Starting with existing clients imports them silently; disconnect and reconnect an already known client and verify no `new_device` entry. Use a genuinely unseen/test MAC to verify exactly one entry after `sta-authorized`. Nearby probes and pre-authorization events must not add devices. Reload Wi-Fi **only during an approved test window**: subscription must recover within the reconciliation/retry intervals and known devices must not be rediscovered.

Inspect state after stopping, then run the same monitor again with the same file:

```sh
ssh "$ROUTER" '/tmp/wifi-guard -state /tmp/wifi-guard-test/state.json -dump'
```

Do not paste registry/log output publicly: it includes device identifiers. After successful validation and explicit approval, use `-state /etc/wifi-guard/state.json` for reboot persistence (the default). The daemon creates its state directory with mode 0700 and files with 0600. Existing state paths must belong to the daemon user; the directory must not be group/world writable, and the file must be private and not a symlink. Use a dedicated directory, never a state file directly in shared `/tmp`. It does not change UCI or firewall settings. Automatic startup is deferred to Phase D.

## CLI

- `-ubus ubus`: executable path, invoked directly without a shell.
- `-state /etc/wifi-guard/state.json`: persistent device registry and bounded history.
- `-bootstrap import`: first-run silent import; `notify` emits new-device logs for baseline clients too. Applies to an uninitialized registry only, never resets known devices.
- `-reconcile 60s`: AP rediscovery and authorized-client snapshot interval.
- `-retry 5s`: retry failed subscriptions / incomplete bootstrap.
- `-resubscribe 5m`: renew even unchanged AP names to recover stale object-ID subscriptions; renewal is checked during reconciliation.
- `-command-timeout 5s`: timeout **per** discovery/snapshot command; idle subscriptions have no artificial timeout.
- `-flush 30s`: batch critical identity/lifecycle writes.
- `-checkpoint 15m`: persist last-seen-only changes less frequently; SIGINT/SIGTERM flush both.
- `-history-limit 500`: maximum lifecycle history records (1–10000).
- `-dump`: read state as JSON and exit; no ubus or network calls.

`new_device` currently means **a log entry**, not a Telegram notification. Missing IP, hostname, SSID or band are omitted rather than invented. Registry trust defaults to `unknown`: known does not mean trusted. Rename/trust mutations are not exposed until a protected LuCI/RPC bridge exists.

Initial import has a finite startup event window: events queued during the first snapshot attempt are imported silently. If one AP fails, later live authorizations still alert; only that AP's first successful snapshot remains silent. Pending baseline APs are persisted across restarts. A client first observed during the silent startup window cannot reliably be distinguished from an existing one.

## Security and durability limits

- MAC identity is best-effort, not authentication. Private MAC rotation can appear as a new device. Trust will suppress alerts, not grant network access. No automatic blocking is implemented.
- Only `sta-authorized` or snapshot `authorized: true` counts as a connection. `probe`, `auth`, `assoc` and DHCP/ARP observations do not.
- State uses temp-file + fsync + rename + parent-directory fsync. Dirty state is batched to reduce flash writes. Sudden power loss/SIGKILL before a flush can lose recent observations and cause a repeated discovery after restart; graceful restart preserves them. Back up the state before upgrades.
- A corrupt/unsupported state file is fatal; never silently discard it. Check filesystem space and permissions before restoring a backup. File writes are retried at the next flush; a failed shutdown save exits nonzero.
- Full registry is retained for deduplication; history is bounded, not the number of known MACs. Monitor growth on constrained flash. Loading and saving are limited to 16 MiB. Oversized saves fail before replacing the previous working file; no MACs are automatically evicted. Capacity exhaustion requires operator intervention (backup/export and a deliberate capacity decision); recent in-memory changes are not durable while writes fail.
- Run one daemon per state file. Local privileged operators can read sensitive MAC history. There is no remotely accessible RPC/HTTP API in Phase A.
- `AGENTS.md` is a local operator brief with observed device data and is intentionally gitignored, not deleted.

## Troubleshooting

- `no_access_points`: APs may be disabled/reloading; inspect `ubus list 'hostapd.*'`. Retries continue.
- `discovery_failed` / `reconcile_failed`: verify ubus permissions, daemon availability and `get_clients` schema. A failed snapshot does not mark that AP's clients offline. An unavailable AP can leave stale online state until recovery.
- `subscription_failed`: EOF, subprocess failure or oversized input causes resubscription. Malformed/irrelevant JSON lines are ignored. Supported framing is one event JSON object per line, as emitted by the ubus CLI.
- No new-device log: it may already be known, it may be from an initial snapshot still pending for a failed AP, or the event lacks `ifname`. Reconciliation can recover missed authorizations.
- `state_save_failed`: fix storage/permissions; the daemon retains dirty state in memory and logs retries. Do not assume logs mean a durable write.

## Uninstall

Stop the manually started process. Remove `/tmp/wifi-guard` and temporary test state only if no longer needed. Preserve `/etc/wifi-guard/state.json` unless you intentionally want to reset known-device identity. No service/package/UCI/firewall changes are made by this phase.

See [architecture](docs/architecture.md) and [verification](docs/verification.md). MIT licensed.
