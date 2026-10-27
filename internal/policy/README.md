# Standalone persistent deny engine

This package does not start a goroutine or touch a router on `Open`. Integration
must opt in and supply a protected administrator MAC list. It implements a saved
MAC deny until explicit unblock, not strong device authentication.

## Wiring

- `Open(Options{Path: ..., ProtectedMACs: ...}) (*Engine, error)` opens a private
  journal and takes its nonblocking kernel lock. The existing parent directory
  must be owned by the effective user and private (0700); files are regular,
  singly linked, owned 0600. Close the engine after its worker stops. Never unlink
  the lock file. `ConfigDir`, `UCI`, `UBus`, `ControlDir` default to `/etc/config`,
  `uci`, `ubus`, `/var/run/hostapd`.
- `SetDenied(ctx, mac, bool) (Status, error)` durably records intent before any
  policy side effect, then attempts reconciliation. A returned error can still
  mean the requested intent is durable or publication happened. Inspect Status
  and retry reconciliation; do not assume rollback.
- `Reconcile(ctx) error` is explicitly required after startup and periodically
  thereafter (including hostapd recovery). Saved desired state is not silently
  discarded. Conflicts with current protection settings fail before writes.
- `Status(mac) Status` is a nonblocking, cached read. `Desired` is intent;
  `Applied` means the latest reconciliation verified that intent. An untracked
  MAC initially returns zero status; a no-op unblock returns Applied true.
  Applied false after startup means reconciliation has not verified it yet.
- `Close() error` releases the lock and refuses subsequent mutations.

Use a bounded, serialized background worker, not the registry owner goroutine,
for SetDenied/Reconcile. Status is safe to read concurrently. Engine methods
serialize mutations, but waiting for that mutex is not context-interruptible;
use a bounded queue and service-lifetime contexts for retries. Each subprocess
has a five-second limit, socket I/O three seconds, and the caller must bound the
whole multi-AP operation. No callbacks, scheduler, registry changes or CLI are
implemented here.

## Scope and recovery

All supported active, named UCI AP sections must pass preflight before the first
wireless publication. Allow mode, populated inactive maclists, external ACL
files, nonempty runtime ACCEPT_ACL, VLAN entries, ambiguous mappings and missing
owned APs fail closed. Station, mesh, disabled, WDS and backhaul-only sections
are not modified. A successful empty desired policy with no owned cleanup work
needs no backend access or journal write. Stable reconciliations do not rewrite
the journal.

UCI edits are prepared in an isolated private staging directory with libuci,
re-exported and checked for a desired-state fixed point. Publication preserves
wireless mode/owner and uses fsync + atomic rename + directory fsync, guarded by
a final inode/content/metadata drift check. The journal stores before/after
hashes and proposed ownership before publication. Recovery adopts ownership
only when the current wireless bytes match one of those hashes. Other drift
requires operator investigation; it never restores a whole old wireless file.
Existing `.bak` files are neither consumed nor overwritten. No CLEAR, wireless
reload, service restart, or all-client operation is used.

Runtime uses connected Unixgram with a random Linux abstract local address;
a filesystem client socket in a root-private directory is incompatible with
nonroot hostapd replies. PING, DENY_ACL SHOW and ACCEPT_ACL SHOW are read-only
preflight operations. Mutations are only target DENY_ACL ADD_MAC/DEL_MAC and a
target ubus del_client with deauth/reason, **without ban_time**. The persistent
ACL already prevents reconnects: creating a temporary ubus ban would add an
ownership ambiguity on unblock. External bans are never cleared. A legacy
journal with an active owned transient ban waits until its recorded expiry
rather than risk clearing an externally replaced ban.

Unblock removes only owned UCI/runtime entries, restoring a mode transition only
when no external entry remains. A pre-existing runtime deny is recorded before
UCI publication and is not claimed after crash/restart. External restrictions
can intentionally leave the target denied after our unblock; Applied true means
our requested policy is absent, not that another administrator's rules allow it.

## Limits and verification

The wireless drift check is not a cross-process compare-and-swap. Serialize
other wireless writers operationally; an external edit in the final check/rename
window cannot be made safe with a private journal lock. Same-MAC external ACL
replacement after our ownership was recorded is also indistinguishable in
hostapd's MAC-only control protocol. Missing owned runtime interfaces fail closed
rather than assuming cleanup succeeded. fsync reduces power-loss risk but cannot
make journal/config/runtime changes one atomic transaction. Status describes the
last readback, not continuous enforcement or an immutable hostapd generation.
MAC randomization/spoofing can bypass this identity-based policy.

Tests use synthetic MACs, fake UCI/ubus executables, and local real Unixgram peers.
They do not verify router-side mutations, real libuci staging behavior, hostapd
restart/reload behavior, or disconnection of a real client. Those require a
separately approved hardware window. No router access or credentials are needed
for `go test -race ./internal/policy`.
