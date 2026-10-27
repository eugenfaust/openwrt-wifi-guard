# AGENTS.md — OpenWrt WiFi Guard contributor guide

## Scope and current state

WiFi Guard is a router-native Go service that discovers **authorized Wi-Fi** clients, records a persistent device registry and bounded history, sends Telegram alerts, accepts opt-in authenticated private-chat decisions, and presents a native LuCI view. Ethernet clients and nearby probe/auth/assoc attempts are not device discoveries. Unknown clients are never blocked automatically. The current package definitions are `wifi-guard` and `luci-app-wifi-guard` version `0.3.0-r5`, including read-only DHCP hostname enrichment and automatic LuCI/rpcd installation refresh, for OpenWrt 24.10.2 mediatek/filogic (`aarch64_cortex-a53` and `all`). Version r5 is locally tested/built only; the last installed and payload-verified router version is r4. Do not assume a new package or commit has been deployed merely because it builds locally.

The approved AX3000T deployment has working notifications, callbacks and confirmed target-only MAC blocking/unblocking. A real phone block, saved UCI + live ACL enforcement on both active APs, monitor-only restart, explicit unblock and manual Wi-Fi reconnect were verified. **No router/hostapd/Wi-Fi restart or power-loss recovery was verified**; do not claim those acceptance gates passed. Consult `PLAN.md`, `docs/verification.md` and `docs/telegram-actions.md` before changing policy behavior.

## Repository map

- `cmd/wifi-guard`: daemon flags, private RPC executable and control bootstrap.
- `internal/monitor`: dynamic hostapd discovery, object subscriptions (`ubus subscribe`, not global `ubus listen`), snapshots, reconciliation and alerts.
- `internal/devices`: MAC normalization, lifecycle, trust metadata, persistent review nonce, decision revision and desired policy.
- `internal/storage`: private atomic JSON state, size/permission checks and exclusive adjacent registry lock.
- `internal/telegram`: bounded delivery/test queue, Bot API polling and signed, chat-bound, expiring callback claims.
- `internal/config`: restricted private UCI Telegram settings with staging/readback and post-publication uncertainty handling.
- `internal/app`: single registry owner, separate bounded configuration/policy workers, credential generations and private decision routing.
- `internal/policy`: exclusively locked ownership/intent journal, conservative staged `wireless` UCI projection, target-only hostapd control ACL and targeted disconnect/readback.
- `internal/rpc`: root-private Unix socket with an explicit public method allowlist; rpcd provides native session authorization.
- `luci/`: native JS view, rpcd executable bridge, menu and ACL. `packaging/openwrt/`: official SDK `.ipk` definitions and procd init.
- `tests/`: compiled fake-ubus smoke and LuCI contract tests. `docs/`: architecture, security, installation, action operations and actual verification.

## Invariants

1. Only `sta-authorized` or a `get_clients` entry with `authorized: true` establishes a connection. Ignore probes/auth/assoc; deduplicate reconnects and roaming. Perform positive snapshot updates before negatives. Failed AP discovery/snapshots do not mark clients offline; successful empty discovery does. Keep initial import silent and finite.
2. Mutate the registry only on its owner loop. Save a review nonce **before** publishing buttons. Authenticate both Telegram sender and positive private chat, verify credential generation and current nonce/revision, and persist intent before acknowledging it. A stale Trust callback must never remove a later deny. Trust is metadata, not network authorization.
3. Blocking is opt-in and needs separate confirmation plus explicitly protected administrative MACs. It is a saved MAC deny until manual unblock, **not** a temporary ubus ban. The protected list is independent of trusted status. Keep a wired/local LuCI recovery route; don't depend on Telegram as the only way to unblock.
4. Preserve independently configured ACLs. Reject unsupported allowlists, ACL files/overrides, ambiguous mappings or foreign-rule drift rather than replacing their policy. Target-only `DENY_ACL ADD_MAC`/`DEL_MAC` and targeted `del_client` are allowed only in an authorized block/unblock flow; never `CLEAR`, blanket disconnect or service reload as a shortcut. Record ownership before mutations and report partial/uncertain enforcement as pending, not applied. MACs are spoofable identifiers.
5. Keep UCI/fsync and hostapd control I/O on bounded workers, not the monitor owner loop. Treat an error after rename as **possibly published**; recover/read back with a service-lifetime context and fail closed until state is known. Preserve in-memory notification queue for no-op settings updates and honor Telegram `retry_after` across sends and callback writes.
6. No unauthenticated TCP/HTTP server. Native rpcd ACLs grant read `devices/history/settings` and restricted write `rename/trust/unblock/configure/test`; internal `_telegram_action` and `_policy_*` owner commands are never exposed through public RPC. Avoid raw bot replies, tokens, private MACs, or full registry contents in public logs and docs.
7. Keep private config/registry/journal and process locks restrictive. Lock inodes must not be unlinked while held. An update that could block access to the router requires a separately approved exact test client and recoverable path. Do not change router DNS, routing, Wi-Fi, hostapd, netifd, firewall or ubus services for routine tests.

## Changes and verification

Write code/primary docs in English and keep `README.ru.md` in sync; communicate progress in Russian. Use synthetic locally administered MACs in tracked fixtures and documents. Do not commit credentials, router addresses, observed client identifiers, private backups or diagnostics. The target/router access and credentials are intentionally not described in this public file; load the local `openwrt-wifi-guard-development` skill and its private router-access reference before authorized live work. Never infer network state from a successful build.

Install Go 1.23+ and ensure `go` is on `PATH`. From the repository root:

```sh
make check smoke arm64
GOTOOLCHAIN=go1.23.0 go test ./...
# On a Linux x86-64 host with the official pinned SDK prerequisites:
make packages
```

`make check` includes gofmt, vet, race tests, LuCI tests and shell syntax. The smoke harness uses a fake ubus; `arm64` is a cross-build, not hardware proof. Inspect official SDK package metadata and installed payload hashes separately. Keep CI and real hardware results distinct. Network-affecting tests require a selected noncritical client, pretest backups/config hashes/PIDs, per-AP runtime and UCI readback, manual unblock and final recovery confirmation. Never publish raw router state. Record exact checks, failures and still-open gates in `docs/verification.md`; update `PLAN.md` and both READMEs when shipped behavior changes.
