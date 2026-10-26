# Implementation plan

## Phase A — implement and validate now
1. Build a stdlib-only Go module and CLI with safe, explicit file paths.
2. Implement validated MAC normalization, known/unknown distinction, first-run silent import, roaming-safe deduplication, bounded history and per-interface reconciliation.
3. Persist versioned state atomically with restrictive modes, periodic batching and shutdown flush. Refuse corrupt or unsupported state rather than silently resetting identity.
4. Discover hostapd AP objects; consume JSON from `ubus subscribe`, not `ubus listen`. Ignore probe/auth/assoc. Bound command timeouts and line sizes; restart subscriptions after exit/object changes.
5. Add test-first coverage for lifecycle, malformed events, persistence, partial failures, cancellation and ubus recovery. Exercise the actual CLI against a fake ubus process; build linux/arm64.
6. Document installation/manual SSH verification and limitations in English/Russian. Record verified commands and outstanding hardware checks.

## Gate: real AX3000T validation
Run the documented commands using the actual router address and approved access. Confirm get_clients format, subscription framing, idle subscription survival, roaming and Wi-Fi reload. No automatic router configuration changes.

## Subsequent phases — after the gate
- B: restricted UCI configuration, bounded asynchronous Telegram delivery, fake-server tests.
- C: native LuCI devices/history/settings, restricted rpcd bridge and access-control tests; no public HTTP server.
- D: separate daemon/LuCI OpenWrt 24.10 .ipk packages via official SDK, procd lifecycle and CI.

Phase A is not the full product: Telegram, LuCI, procd and .ipk delivery are intentionally not claimed yet.

## Current status

Phase A implementation and local validation are complete, including regression fixes from independent review. Non-disruptive AX3000T checks passed, including real phone authorization, known-client deduplication, and one new-device discovery using an isolated registry. The disruptive reload/roaming and persistent-installation portions of the hardware gate remain open. See `docs/verification.md` for executed checks and exact hardware steps; do not start router deployment or declare full-product acceptance from host tests alone.
