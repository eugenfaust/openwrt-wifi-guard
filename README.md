# OpenWrt WiFi Guard

Router-native detection of newly authorized Wi-Fi clients, Telegram alerts, and a native LuCI device registry. Go standard library, static ARM64 binary, no CGO, no new public listening port. Ethernet-only clients are not monitored and unknown devices are never automatically blocked.

[Русский](README.ru.md) · [Installation](docs/installation.md) · [Security](docs/security.md) · [Verification](docs/verification.md)

## Implemented

- Dynamic `hostapd.*` discovery and **object subscriptions**, not global `ubus listen`.
- Successful `sta-authorized` events and periodic `get_clients` reconciliation; probes/auth/assoc alone never add devices.
- Silent first-run import, known/trusted distinction, MAC normalization and roaming/reconnect deduplication.
- Versioned atomic registry, bounded history, batched flash writes and graceful shutdown.
- Read-only DHCP hostname enrichment for known Wi-Fi clients. Display/search and Telegram use the operator-saved name first, then the hostname; hostname discovery never grants trust. Devices without a trust decision are labeled **Trust not set**.
- Bounded asynchronous Telegram delivery, retries/backoff/timeouts, a labeled delivery test and restricted UCI settings.
- Separate native LuCI Overview, Devices, History and Settings routes. Overview shows current authorized-client statistics and recent activity; Devices supports name/MAC/interface search and connection/trust/deny filters, rename/trust and confirmed unblock; History paginates the latest 500 events locally (25 per page); Settings retains token-safe save/test controls. Russian UI translations are included.
- Native rpcd authentication/ACL with a private 0600 Unix bridge; no generic exec/file/UCI access granted to limited roles.
- Separate official-SDK `.ipk` packages for the daemon and LuCI, plus procd startup/reload/respawn.

**Verified target:** Xiaomi AX3000T, OpenWrt 24.10.2, mediatek/filogic, `aarch64_cortex-a53`. Native installation, UI actions, access denial and package reinstall preserving data were tested. Real labeled-test and isolated automatic-notification receipts were confirmed by the operator; automated regression tests use a fake server. Wi-Fi/hostapd restarts and router reboot were deliberately not exercised on a live household Internet connection. See the detailed verification report rather than treating cross-compilation or CI configuration as hardware proof.

## Install through LuCI — no SSH required

**Supported:** OpenWrt 24.10, mediatek/filogic, `aarch64_cortex-a53`, with IPK/opkg. These are not packages for arbitrary OpenWrt targets or APK-based firmware; the LuCI package's `all` architecture does not broaden daemon support.

**Upcoming 0.3.0-r5:** automatic rpcd/LuCI refresh is new and **not yet live-tested on hardware**. Existing approved-router results are historical r4-and-earlier verification, not r5 installation proof.

1. Download both matching files from the [r5 preview release](https://github.com/eugenfaust/openwrt-wifi-guard/releases/tag/v0.3.0-r5) to your computer: `wifi-guard_0.3.0-r5_aarch64_cortex-a53.ipk` and `luci-app-wifi-guard_0.3.0-r5_all.ipk`.
2. In LuCI, confirm firmware/target under **Status → Overview** and check free storage under **System → Software** (also check free memory in Status). Keep several MiB of spare flash beyond the package footprint. Before upgrading, back up configuration through **System → Backup / Flash Firmware** and have an advanced administrator privately back up `/etc/config/wifi-guard` and the complete `/etc/wifi-guard/` directory; the standard backup may not include the dynamic registry.
3. Open **System → Software → Update lists**. Internet access and matching firmware feeds are needed for dependencies. Resolve failures first; never force dependencies or install on an unsupported target.
4. Choose **Upload Package** and install `wifi-guard` first. Wait for success, then upload/install `luci-app-wifi-guard` of the same release.
5. Refresh the browser or log in again, then open **Services → WiFi Guard → Settings** to configure Telegram.

OpenWrt's default postinstall enables/starts only `wifi-guard`; the r5 LuCI package postinstall automatically refreshes rpcd and LuCI caches. No manual SSH command or rpcd reload is required for normal installation. Network, Wi-Fi/hostapd, firewall, DHCP and DNS services are untouched. See [installation](docs/installation.md) for the complete UI path, upgrade precautions and secondary advanced SSH options.

Start a private conversation with your Telegram bot, enter its numeric chat ID and token directly in the authenticated settings page, enable delivery, save and send a test. Confirm receipt in Telegram; API acceptance alone is not receipt. The token field stays empty on subsequent reads: blank preserves it, explicit clear removes it. Do not send credentials in chat or put them in process arguments. Use HTTPS or a trusted management connection; HTTP does not encrypt the token.

## Developer build — optional

Go 1.23+; Make, Python 3 and Node.js 20+ are development tools only.

```sh
make check smoke arm64
# Linux x86-64; official SDK plus documented host prerequisites:
make packages
```

Outputs:
- `build/wifi-guard`: development-host binary.
- `build/wifi-guard-linux-arm64`: static router binary.
- `build/packages/wifi-guard_0.3.0-r5_aarch64_cortex-a53.ipk` (upcoming release)
- `build/packages/luci-app-wifi-guard_0.3.0-r5_all.ipk` (upcoming release)

Build/test success is not hardware installation proof. Historical r4 router verification includes all four LuCI routes and payload/config preservation; r5 remains unverified on hardware. Russian catalog coverage is tested locally; prior live browser checks used the unchanged English language preference.

## Advanced optional SSH: manual, nonpersistent test

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

## Advanced optional CLI and native control

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

A MAC is best-effort identity: rotation can appear new. Known does not mean trusted, and trusted is not network authorization. Opt-in signed private-chat callbacks and confirmed saved MAC deny/unblock are described in [Telegram decisions](docs/telegram-actions.md); rollout and hardware acceptance are separate from the previously installed outbound-only release. Vendor lookup, repeated-appearance alerts and SSID/band/IP enrichment are not implemented. Hostnames are best-effort DHCP metadata, not authenticated identity: a client may supply none or an arbitrary name. Clearing a manual name reveals the DHCP hostname when available. New-device alerts use the hostname available at delivery/enqueue time; a later DHCP update does not resend an alert.

The startup import window is finite. Pending first snapshots are tracked per AP and persisted; one failed AP cannot indefinitely silence later live authorizations. Failed discovery/snapshots do not mark clients offline; a successful empty AP discovery does. Buffered events are drained before subscription replacement/shutdown.

State/config are atomically replaced with restrictive permissions. Sudden power loss before a batch can lose recent observations. Published-but-unconfirmed fsync failures remain visible and dirty for retry instead of rolling memory back behind the file. History is bounded; known MAC identities are not automatically evicted. Matching 16 MiB registry read/write limits fail before replacing the previous valid file when capacity is exceeded.

Telegram's in-memory queue is bounded and best-effort, not a durable outbox. Overflow/shutdown can lose pending alerts; an ambiguous network timeout after API acceptance can duplicate a retry. Reconnect deduplication does not promise exactly-once external HTTP delivery. Credentials and raw API replies are never logged. See [security](docs/security.md) before deployment.

## Troubleshooting and uninstall

- **Monitor unavailable:** inspect `/etc/init.d/wifi-guard status`, `logread -e wifi-guard`, config ownership/mode and `ubus list 'hostapd.*'`.
- **Missing LuCI object/menu:** first refresh/re-login and check package installation errors in LuCI. r5 refreshes rpcd/caches automatically. Advanced optional SSH fallback: inspect `ubus -v list wifi-guard` and reload only rpcd. Do not reset the network.
- **Telegram failure:** start the bot first, verify the numeric chat ID/token in LuCI, inspect generic failure logs, and use the test control. The token is never returned by settings reads.
- **Storage errors:** check space/permissions and restore a private backup if corrupt; identities are never silently reset.
- **No new-device alert:** the client may already be known, only probing, or imported in its initial silent window. A failed alert is not automatically resent forever after restart.

To remove through LuCI, stop/disable only WiFi Guard under **System → Startup**, then remove `luci-app-wifi-guard` followed by `wifi-guard` under **System → Software**. Advanced optional SSH equivalent: stop only `wifi-guard`, then `opkg remove luci-app-wifi-guard wifi-guard`. Modified config and the dynamically created registry may remain to preserve credentials/known identities; remove them only deliberately after backup. Upgrade conffiles can create an `-opkg` sidecar while preserving your active config. No uninstall step should alter wireless/firewall/DHCP/DNS. Command-line diagnostics above are optional advanced administration, not installation requirements.

The tracked `AGENTS.md` is a sanitized public contributor guide; private operator data and backups do not belong in Git. MIT licensed.
