# OpenWrt WiFi Guard

Router-native detection of newly authorized Wi-Fi clients, Telegram alerts, and a native LuCI device registry. Go standard library, static ARM64 binary, no CGO, no new public listening port. Ethernet-only clients are not monitored and unknown devices are never automatically blocked.

[Русский](README.ru.md) · [Installation](docs/installation.md) · [Security](docs/security.md) · [Verification](docs/verification.md)

## Implemented

- Dynamic `hostapd.*` discovery and **object subscriptions**, not global `ubus listen`.
- Successful `sta-authorized` events and periodic `get_clients` reconciliation; probes/auth/assoc alone never add devices.
- Silent first-run import, known/trusted distinction, MAC normalization and roaming/reconnect deduplication.
- Versioned atomic registry, bounded history, batched flash writes and graceful shutdown.
- Bounded asynchronous Telegram delivery, retries/backoff/timeouts, a labeled delivery test and restricted UCI settings.
- LuCI devices/status/history, rename/trust actions, token-safe settings and test controls.
- Native rpcd authentication/ACL with a private 0600 Unix bridge; no generic exec/file/UCI access granted to limited roles.
- Separate official-SDK `.ipk` packages for the daemon and LuCI, plus procd startup/reload/respawn.

**Verified target:** Xiaomi AX3000T, OpenWrt 24.10.2, mediatek/filogic, `aarch64_cortex-a53`. Native installation, UI actions, access denial and package reinstall preserving data were tested. Real labeled-test and isolated automatic-notification receipts were confirmed by the operator; automated regression tests use a fake server. Wi-Fi/hostapd restarts and router reboot were deliberately not exercised on a live household Internet connection. See the detailed verification report rather than treating cross-compilation or CI configuration as hardware proof.

## Build and install

Go 1.23+; Make, Python 3 and Node.js 20+ are development tools only.

```sh
make check smoke arm64
# Linux x86-64; official SDK plus documented host prerequisites:
make packages
```

Outputs:
- `build/wifi-guard`: development-host binary.
- `build/wifi-guard-linux-arm64`: static router binary.
- `build/packages/wifi-guard_0.2.0-r2_aarch64_cortex-a53.ipk`
- `build/packages/luci-app-wifi-guard_0.2.0-r2_all.ipk`

Follow [installation](docs/installation.md) using the actual approved SSH destination, available flash/RAM and existing runtime dependencies. Do not assume a default router address. OpenWrt postinstall enables/starts only this monitor; native rpcd may need a management-only reload to register the new plugin. Network/Wi-Fi/firewall/DNS reloads are not required.

In LuCI, open **Services → WiFi Guard**. Start a conversation with your Telegram bot, then enter the numeric chat ID and token directly in the authenticated settings page. Enable delivery, save, and send a test. The token password field is empty on subsequent reads: blank preserves it, explicit clear removes it. Do not send credentials in chat or put them in process arguments. An origin-bound browser vault is not a generic server-side token export interface.

## Manual, nonpersistent test

Use a private directory in RAM; this does not install a service or alter UCI/network settings. Copy the binary to the actual router first:

```sh
# On the router, using a fresh private test directory:
umask 077
mkdir -m 700 /tmp/wifi-guard-test
# Place the ARM64 binary at /tmp/wifi-guard-test/wifi-guard, then:
chmod 700 /tmp/wifi-guard-test/wifi-guard
GOMEMLIMIT=12MiB GOGC=50 /tmp/wifi-guard-test/wifi-guard \
  -state /tmp/wifi-guard-test/state.json \
  -socket /tmp/wifi-guard-test/run/control.sock
```

Ctrl-C flushes and stops only the monitor and its subscription. `/tmp` state does not survive reboot. An exclusive adjacent writer lock rejects a second daemon using the same state file, even with RPC disabled. Read-only dumps remain available. On a production-configured router the default UCI settings apply; use a separate private `-uci-dir` for an isolated disabled-notification test.

## CLI and native control

- `-state /etc/wifi-guard/state.json`: persistent registry/history; `-dump` prints only device state.
- `-ubus ubus`, `-uci uci`, `-uci-dir /etc/config`: fixed-argv executable/config locations.
- `-socket /var/run/wifi-guard/control.sock`: private control socket; an empty value disables it.
- `-bootstrap import`: silent first observation window; `notify` treats initial clients as new. Neither resets saved identities.
- `-reconcile 60s`, `-retry 5s`, `-resubscribe 5m`: AP recovery and periodic stale-subscription renewal.
- `-command-timeout 5s`: each ubus/UCI command, not an idle subscription.
- `-flush 30s`, `-checkpoint 15m`: critical lifecycle persistence versus last-seen-only checkpoints.
- `-history-limit 500`: bounded history (1–10000 entries); the UI returns up to 500 newest events per call.
- `-test-telegram`: use already securely provisioned UCI credentials, send a labeled test and exit.

`wifi-guard rpc list` exposes the restricted executable-plugin schema. `wifi-guard rpc call METHOD` reads parameters from stdin and forwards to the private socket. This is privileged local control, not an unauthenticated network API. The plugin strips native transport session metadata only after rpcd performs its ACL check; application parameter validation remains strict.

## Reliability and security limits

A MAC is best-effort identity: rotation can appear new. Known does not mean trusted, and trusted is not network authorization. Blocking, Telegram callbacks, vendor lookup and repeated-appearance alerts are not implemented. SSID/band/IP/hostname enrichment is deferred; messages contain reliable MAC, UTC timestamp and interface only.

The startup import window is finite. Pending first snapshots are tracked per AP and persisted; one failed AP cannot indefinitely silence later live authorizations. Failed discovery/snapshots do not mark clients offline; a successful empty AP discovery does. Buffered events are drained before subscription replacement/shutdown.

State/config are atomically replaced with restrictive permissions. Sudden power loss before a batch can lose recent observations. Published-but-unconfirmed fsync failures remain visible and dirty for retry instead of rolling memory back behind the file. History is bounded; known MAC identities are not automatically evicted. Matching 16 MiB registry read/write limits fail before replacing the previous valid file when capacity is exceeded.

Telegram's in-memory queue is bounded and best-effort, not a durable outbox. Overflow/shutdown can lose pending alerts; an ambiguous network timeout after API acceptance can duplicate a retry. Reconnect deduplication does not promise exactly-once external HTTP delivery. Credentials and raw API replies are never logged. See [security](docs/security.md) before deployment.

## Troubleshooting and uninstall

- **Monitor unavailable:** inspect `/etc/init.d/wifi-guard status`, `logread -e wifi-guard`, config ownership/mode and `ubus list 'hostapd.*'`.
- **Missing LuCI object/menu:** check `ubus -v list wifi-guard`, reload only rpcd and refresh/re-login to LuCI. Do not reset the network.
- **Telegram failure:** start the bot first, verify the numeric chat ID/token in LuCI, inspect generic failure logs, and use the test control. The token is never returned by settings reads.
- **Storage errors:** check space/permissions and restore a private backup if corrupt; identities are never silently reset.
- **No new-device alert:** the client may already be known, only probing, or imported in its initial silent window. A failed alert is not automatically resent forever after restart.

To remove, stop only `wifi-guard`, then `opkg remove luci-app-wifi-guard wifi-guard`. Modified config and the dynamically created registry may remain to preserve credentials/known identities; remove them only deliberately after backup. Upgrade conffiles can create an `-opkg` sidecar while preserving your active config. No uninstall step should alter wireless/firewall/DHCP/DNS.

The local operator `AGENTS.md` is intentionally gitignored because it contains observed device data. MIT licensed.
