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

## Phase D — IPK/procd: r2 baseline; 0.3.0-r1 now installed

Separate real official-SDK packages for ARM64 daemon and architecture-independent LuCI. Native installation, enabled/running procd service, root:0600 state/config/socket and reinstall preservation of config/identity/first_seen/name/trust passed. CI definition includes tests/SDK/artifact upload but no remote CI run is claimed. Release artifacts are under build/packages (gitignored).

## Remaining acceptance gates / follow-on

- Real test-message and isolated automatic detection-to-Telegram receipt were both confirmed. Automatic notifications are enabled on the installed service.
- Disruptive hostapd/Wi-Fi restart, AP removal, band roaming and router reboot only in a separately approved window; not required just to add alerts/UI.
- Long-run flash/RAM/network-failure durability monitoring. Current outbox is bounded in-memory best-effort, not durable exactly-once delivery.
- Repeat-appearance alerts, vendor/SSID/IP/band enrichment and additional target architectures remain follow-on. DHCP hostname enrichment and Russian UI localization are implemented below. Automatic blocking is deliberately absent; confirmed persistent MAC deny is implemented separately below.

## Hardening integrated in r2

Exclusive registry writer lock; nonblocking serialized UCI worker; independent post-publication recovery; no-op queue preservation; credential-generation cancellation and shared Telegram tests; server retry_after cooldown; RPC disconnect cancellation; fail-safe LuCI settings recovery. Local regressions, ARM64/minimum-Go builds, official SDK artifacts and native upgrade/no-op/lock/UI checks passed.

## Telegram action decisions — 0.3.0-r1 implemented and installed

Signed private-chat callbacks, persisted nonce/review transitions, two-step block/unblock, isolated policy worker, owned persistent UCI deny/runtime ACL enforcement and native LuCI unblock are implemented. Local race/smoke/minimum-Go tests and official SDK builds passed. An isolated real-bot synthetic-device test received Trust and verified it by reloading saved state. Both installed package payloads match 0.3.0-r1 artifacts; callbacks and confirmed blocking are enabled. All 18 existing registry identities were conservatively protected from Telegram blocking without marking them trusted. Network config hashes/service PIDs remained unchanged; WAN remained up. A separately approved phone test now verified a target-only persistent block on both APs, saved deny across a WiFi Guard monitor restart, and target-only unblock with original UCI network hashes/protection restored. The phone was observed authorized again after a manual Wi-Fi reconnect. No disruptive Wi-Fi/hostapd/router reboot test was performed. See `docs/telegram-actions.md` and the exact verification record; these remaining hardware gates are not declared closed.

## UX/UI refactor — 0.3.0-r2, local implementation

Native LuCI now has Overview, Devices, History and Settings routes using the existing theme. Device search and connection/trust/deny filters, current connection statistics, bounded history pagination (25 per page, latest 500) and Russian translations are implemented. Rename/trust/confirmed unblock and fail-safe token settings are preserved; the public RPC schema and ACL are unchanged. Telegram notifications include saved friendly names; callbacks edit the existing message instead of sending another one. Router-local timestamps use bounded libc-backed date conversion with explicit UTC fallback; stale device actions are removed after failed refresh, and uncertain settings saves require a page reload. The final combined tree passed `make check smoke arm64` and minimum Go 1.23 tests. Both 0.3.0-r2 official OpenWrt 24.10.2 SDK packages built and their metadata/payloads were inspected. The operator-approved upgrade installed both r2 packages on the router; all installed package payloads match the IPKs. Private backup, preserved configuration/device metadata, unchanged network configuration hashes and service PIDs, running/enabled monitor and WAN-up readback were verified. All four routes loaded in the existing Argon theme with no unavailable/error state or horizontal page overflow; the token field stayed empty. Firmware BusyBox event-time conversion worked with the router's current +0000 offset. The native Telegram test returned ok; human receipt and a new interactive callback exercise remain unconfirmed.

## DHCP names and trust labels — 0.3.0-r3

Read-only bounded DHCP enrichment adds separate hostname hints to known Wi-Fi clients, with static assignments before active leases and manual names before either. LuCI display/search and Telegram alerts/callbacks use the name fallback; trust remains an explicit independent decision, labeled "Trust not set". Refresh and expiry run without source I/O on the registry owner; metadata changes use normal persistence batching and create no discoveries or alerts. Local race/smoke/minimum-Go tests and official SDK builds passed. Both r3 packages are installed and payload-verified on the approved router: 8 of 18 known clients now have hostnames, including all 7 matching active named leases. Settings, previous identity/trust/deny metadata, network configuration hashes and network/rpcd PIDs were preserved. Authenticated LuCI rendered the names and new trust label; hostname search and the manual-only rename dialog passed. No network/Wi-Fi/DHCP reload or new real Telegram delivery/callback test was performed.

Independent review also caught a multiline quoted UCI value regression. The test-first fix, repeated full local/SDK checks and non-disruptive r4 upgrade passed; both packages installed on the approved router are `0.3.0-r4`, with the same hostname coverage and preserved settings/network state. All four authenticated LuCI routes were rechecked. This is historical r4 verification, not verification of a later release.

## LuCI-only installation — upcoming 0.3.0-r5, local work; hardware unverified

The documented primary installation path is two matching IPKs: download to the user's computer, LuCI System → Software → Update lists (Internet/matching feeds required), Upload Package for `wifi-guard` first and `luci-app-wifi-guard` second, then browser refresh/re-login and Services → WiFi Guard → Settings for Telegram. Normal installation needs no SSH commands. Advanced SSH instructions remain optional and secondary.

The r5 LuCI package postinstall is intended to refresh rpcd and LuCI caches automatically. OpenWrt's default postinstall enables/starts only `wifi-guard`; network, Wi-Fi/hostapd, firewall, DHCP and DNS services remain untouched. Support stays limited to OpenWrt 24.10 mediatek/filogic, `aarch64_cortex-a53`, IPK/opkg, not arbitrary targets or APK firmware. Documentation requires UI space checks, no forced dependencies, and private config/registry backups for upgrades (the dynamic registry may need advanced administrator backup beyond LuCI's standard archive).

**Acceptance still open:** the new r5 installation/upgrade hooks and end-to-end LuCI-only workflow have not been live-tested on hardware. Local `make check smoke arm64`, minimum-Go tests, the full official SDK build and four sandbox hook tests against the emitted IPKs passed; these do not prove hardware installation. Verify installation order, automatic menu/RPC availability after refresh/login, monitor startup, preserved config/private state and untouched network services in an approved future hardware window before calling r5 router-verified. Do not reinterpret the existing r4 router checks as r5 results.

## LuCI blocking — 0.3.0-r6, local work

Add write-ACL-only confirmed block with expected revision, owner-side protection/enablement checks, computed per-device eligibility and the existing persistent policy worker. Devices shows a separate warning/confirmation and disabled reasons; uncertain results trigger readback, not automatic retry. Telegram settings are not prerequisites; existing protected identities remain protected. Matching daemon/LuCI packages are required. The new browser/router interaction and real target disconnect remain unverified; no live router mutation is part of this task.

See docs/verification.md for executed checks and docs/security.md for explicit limits. Do not declare the full product acceptance closed while the above live gates remain open.
