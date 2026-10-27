# Security model

## Identity and scope

Only authorized Wi-Fi clients are observed. Nearby probe/auth/assoc traffic and Ethernet-only clients do not populate the registry. A MAC is a best-effort identity, not authentication; rotation can produce a new identity. Trusted means known administrator metadata, not permission to use the network. The reserved blocked value is never an automatic blocking policy and cannot be assigned through the API.

## Native LuCI/rpcd boundary

No TCP/HTTP listener is added. Native LuCI performs authentication, and native rpcd checks the `luci-app-wifi-guard` ACL before its executable plugin forwards one allowlisted method to the daemon's Unix socket. Read methods are devices/history/settings; write methods are rename/trust/configure/test. No generic exec, file access, UCI read, ubus wildcard or alternate-object forwarding is granted by this ACL.

The Unix socket is 0600 inside a newly created 0700 directory owned by the service user (root on OpenWrt). Existing directories must have the same owner and no group/other write access; keep ancestor directories trusted too. Root/admin users remain trusted operators. There is no claim of isolation from root, native broad administrator ACLs, or another process running as the same Unix user.

Requests are bounded to 4096 bytes, methods are explicitly allowlisted, unknown parameter fields are rejected, and local concurrency is bounded. Idle clients are closed on daemon shutdown. The monitor owner loop handles registry reads/mutations; outgoing Telegram requests and network tests run separately. Names are limited to 80 Unicode characters without control characters; LuCI renders dynamic data as text, not HTML.

## Telegram secrets

The bot token lives only in restricted `/etc/config/wifi-guard` (0600). UCI writes occur via stdin to a private staged batch; tokens do not appear in process arguments. Read-back verifies staged values before atomic replacement. Empty libuci credential options are treated as empty, not as fatal missing settings. Do not grant generic UCI/file read access to limited LuCI roles.

The settings RPC returns `token_set`, never token bytes. The registry/dump and history contain no token. The UI password field is initially empty; blank preserves a saved token, explicit clear removes it, and enabled notifications require complete valid credentials. Notifications use the fixed official HTTPS origin. Redirects are not followed. API/transport errors are reduced to generic messages instead of echoing secret-bearing URLs or raw Telegram responses. A fake endpoint exists only as a Go constructor test dependency, not as a UCI/CLI/RPC option.

Provision the token directly in authenticated LuCI over a trusted management connection, or use a separately approved secure local provisioning mechanism. Never paste it into chat, put it in command arguments, or commit it. A browser vault entry is origin-bound and is not a general server-side API-secret export facility. HTTP management is not encrypted: use HTTPS or an SSH management tunnel on untrusted networks; there is no automatic TLS-bypass behavior.

## Delivery and durability limits

Telegram has a bounded in-memory queue (32 by default), sequential delivery, three attempts, per-attempt timeout and bounded backoff. Transport failures, 5xx and 429 may retry; terminal 4xx/invalid success responses do not. Queue overflow and final failure are logged without secrets. A permanently slow network cannot block Wi-Fi processing. Configuration UCI I/O uses a separate bounded worker as well. Server `retry_after` is a minimum shared across the notifier; it is not shortened to the application's 30-second exponential-backoff cap. Waiting is cancelable. Successful API acceptance logs only message ID/test flag, not token, recipient or message body.

The queue is not a durable outbox. Shutdown/overflow and genuine credential/configuration changes can discard notifications; unchanged settings preserve the worker/queue. Tests share the same generation gate and are canceled when credentials change. A timeout after Telegram accepted a message is ambiguous and a retry can duplicate it: exact-once external delivery cannot be promised without provider idempotency. Device deduplication prevents ordinary reconnect/roaming alert spam, not ambiguous HTTP-commit duplication. A saved known identity can outlive a failed notification; delivery is not silently retried forever after daemon restart.

Registry lifecycle writes are batched; last-seen-only updates checkpoint less often to limit flash wear. Atomic replacement/fsync does not make unflushed observations survive a power cut. A directory-fsync error can occur after rename: a failed configuration response means it could not be confirmed, not necessarily that no file changed; the daemon reads configuration back using a fresh bounded service context to align live state. If the published state cannot be established, old delivery is stopped and public settings fail closed. Keep backups, monitor explicit storage errors, and serialize external UCI writers with application updates.

## Operational safety

Do not reload Wi-Fi/network/firewall/DNS/hostapd/ubus or disconnect clients for routine tests on a live household router. Restarting only this monitor is sufficient for its lifecycle tests. Live raw MAC history/logs belong in private local diagnostics, never public fixtures. The package does not implement permanent blocking, Telegram callbacks or remote arbitrary commands.
