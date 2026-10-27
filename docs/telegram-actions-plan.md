# Telegram decisions — implementation plan and status

New-device notifications provide **Trust** and **Block**, with saved MAC deny until explicit manual unblock, separate confirmation, and native LuCI recovery. Unknown clients are never blocked automatically.

## Completed implementation

1. **Signed actions:** versioned compact chat-bound HMAC claims, persistent random review nonce, canonical encoding, expiry/future skew and key/credential lifetime checks.
2. **Decision transactions:** authenticate private sender/chat, recheck credential generation and current review on the registry owner, durably save before publishing/acknowledging decisions, reject stale/repeated/superseded actions. Trust does not clear deny.
3. **Telegram worker:** bounded cancelable polling, explicit callback filter, shared cooldown, webhook/conflict/authorization fail-closed behavior, complete confirmation text and refreshed keyboards. No public port or webhook deletion.
4. **Persistent policy worker:** durable ownership/intent journal, staged UCI deny projection, target-only runtime ACL changes/readback and disconnect, restart recovery, conservative external-rule ownership and protected administrative MACs. Unsupported allow/custom/file ACL configurations fail instead of being replaced. New timed bans are not used as persistent policy.
5. **Application/UI:** private in-process internal commands, serialized nonblocking policy I/O, revision-checked native unblock, accurate pending states, no trust shortcut around deny, authenticated LuCI controls and restricted RPC ACL.
6. **Local checks/rollout:** complete race/smoke/minimum-Go and official SDK package builds passed. Isolated real Telegram Trust was saved and verified by state reload. Version 0.3.0-r1 is installed with callbacks/confirmed blocking enabled and all 18 pre-existing identities protected; hashes/PIDs confirm networking was unchanged.

## Remaining acceptance gates

- Choose a separately approved **noncritical real client** for actual deny/disconnect/unblock verification. Runtime ACL mutations and live wireless UCI publication have not been tested on this router; do not claim otherwise.
- Hostapd/Wi-Fi restart, new AP/reboot and power-loss behavior require an approved maintenance window. No network/service restart is needed just to use the deployed buttons.
- Continue long-run flash/RAM/failure monitoring; outgoing notifications remain a bounded best-effort queue, not a durable exactly-once outbox.

Operational details and supported ACL scope: [telegram-actions.md](telegram-actions.md). Exact executed tests, hardware results and exclusions: [verification.md](verification.md). Preserve external allow/deny policy, never perform blanket ACL CLEAR, and never use network/Wi-Fi reload as a blocking/unblocking shortcut.
