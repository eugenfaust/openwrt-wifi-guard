# Telegram decisions and persistent MAC policy

## Scope and rollout

Version 0.3.0 adds opt-in private-chat Trust/Block callbacks. Existing installations remain outbound-only unless explicitly enabled. No new public listener is opened, no webhook is deleted, and no network/Wi-Fi/firewall/DNS/hostapd/ubus service is reloaded. A real confirmed Block deliberately disconnects only its target; do not use it to test a household administrator's phone.

Use a dedicated bot: Telegram does not offer a reliable advance check for another getUpdates consumer. A configured webhook or polling conflict stops callback consumption instead of removing/fighting the other integration. Group outbound messages remain supported, but group callback administration is deliberately unsupported.

## Decisions

- **Trust:** save trust metadata, invalidate the review, do not change network ACLs.
- **Block:** show target MAC and a warning; a separate short-lived confirmation is required. Persist desired deny before applying anything. A stale Trust button cannot unblock a later deny.
- **Unblock:** a separate confirmation removes only Guard-owned rules. Independent administrator restrictions are not removed. Native LuCI also offers confirmed unblock with an expected policy revision, so Telegram is not the only recovery path.
- **Pending/error:** desired policy was saved but enforcement is not verified. This is not successful blocking. Read current policy state rather than treating Telegram's HTTP acknowledgement as network proof.

Callbacks have canonical compact chat-bound HMAC signatures derived from the bot credential, expiry and persistent per-review nonces. Sender and private chat must both equal the configured positive numeric chat ID. Credential changes invalidate old reviews. Confirmation generates a new nonce and expires separately; ordinary trust APIs cannot clear desired deny. Buttons are published only after successful persistence of the corresponding review.

## Enabling controls (privileged operator)

First install/test the new packages with controls disabled. Keep Ethernet/SSH/LuCI recovery available. Select administrative MACs explicitly; alternatively protect the entire existing registry as a conservative baseline, without calling those clients trusted. Never infer an administrator's MAC from a Telegram user ID. The sample MAC below is synthetic, not a device on your router.

```sh
uci set wifi-guard.main.telegram_actions='1'
uci set wifi-guard.main.protected_macs='02:00:00:00:00:01'
# Only after capability/local tests and the chosen protections are checked:
uci set wifi-guard.main.allow_blocking='1'
uci commit wifi-guard
/etc/init.d/wifi-guard restart
```

Only the WiFi Guard monitor is restarted. Controls require an already provisioned enabled Telegram configuration. A protected MAC cannot be blocked through Telegram or the policy engine. Do not enable blocking with a placeholder protected address. Separate multiple real protected MACs with commas. Setting `allow_blocking=0` prevents new Telegram blocks; it is not an unblock operation and does not silently remove already saved denies.

Equivalent daemon flags: `-telegram-actions -allow-blocking -protected-macs <comma-separated-MACs>`. `-policy-state` defaults to `policy.json` adjacent to the registry; `-hostapd-control-dir` defaults to `/var/run/hostapd`. Existing policy journals are opened for reconciliation/unblock even when new blocks are disabled. State and policy journal must be private, backed up together, and owned by this daemon's single writer.

## Persistent enforcement

The policy engine maintains a private, exclusively locked ownership/intent journal. It stages a projection into the `wireless` UCI package and changes only owned target MAC-list entries and necessary owned deny-mode transitions. It verifies publication/recovery before acknowledging an applied policy. At runtime it uses target-only hostapd control ACL changes and a target-only ubus disconnect, with readback; new temporary bans are not created and external bans are not cleared. It never clears an ACL or performs a blanket disconnect. Persisted UCI reloads the deny on later normal Wi-Fi/router startup; loaded runtime ACL entries avoid relying solely on an expiring temporary ban while hostapd remains running.

The initial supported scope is uniquely named, enabled AP sections, excluding station/backhaul/mesh interfaces. Existing deny lists are merged conservatively. **Allow mode, external MAC files, nonempty inactive lists, ambiguous/anonymous sections, VLAN/custom ACL formats and ownership drift are rejected**, not overwritten. Different AP outcomes or missing control capabilities are failures/pending, not universal success. Administrative edits to `wireless` must be serialized with policy updates: hash/readback detection is not a kernel compare-and-swap against an unrelated LuCI writer.

Unblock removes only attributable Guard entries and its own runtime ban. It preserves pre-existing bans/ACLs; if ownership cannot be proved it reports a conflict rather than clearing someone else's restriction. Avoid deleting `policy.json` while owned rules remain. Before uninstall, explicitly unblock Guard-managed devices and check completion; uninstall does not reset `wireless` or silently grant access.

## Verification boundaries

Local fake Telegram, fake ubus/UCI and fake hostapd tests are separate from hardware acceptance. A read-only AX3000T probe verified `PING`, `DENY_ACL SHOW` and `ACCEPT_ACL SHOW` on both active AP sockets. A connected Linux abstract Unix datagram client was required: a root-private filesystem reply directory prevented the unprivileged hostapd from replying. That probe did not add/remove ACLs or disconnect anyone.

Real target blocking, immediate unblock after an actual hostapd restart, power loss and router reboot remain separate explicitly agreed hardware checks. A MAC can be changed/spoofed: this is a saved MAC policy, not permanent identification of a physical device. Exact local commands/results and deployed versions belong in `verification.md`.
