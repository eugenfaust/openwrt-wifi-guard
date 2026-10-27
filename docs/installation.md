# Installation and package lifecycle

## Build

Go 1.23+ is sufficient for the stdlib-only daemon. Node.js 20+ and Python 3 are development-test tools only; neither is needed on the router.

```sh
make check smoke arm64
```

The official OpenWrt 24.10.2 Linux x86-64 SDK builds the initial supported target: mediatek/filogic, `aarch64_cortex-a53`, static `linux/arm64` Go. Other architectures and APK packages are not claimed. The SDK archive SHA-256 is pinned and checked by the script.

```sh
# Development host dependencies, not router packages:
# Debian/Ubuntu: build-essential libncurses-dev zstd python3 rsync gawk curl
make packages
```

The script downloads/resumes the official SDK in `$HOME/.local/share/wifi-guard-sdk`, then invokes its real package build system. The explicit host Go toolchain compiles with CGO disabled; no C ABI/toolchain dependency is introduced by the daemon. `WIFI_GUARD_SOURCE_DIR` identifies the local source; VCS stamping is disabled in the SDK source copy. This is not handcrafted IPK archive generation. The minimal SDK may warn about missing feed metadata; the generated package still records its runtime dependencies, which must already exist or be resolved on the router.

Outputs:
- `build/packages/wifi-guard_0.3.0-r1_aarch64_cortex-a53.ipk`
- `build/packages/luci-app-wifi-guard_0.3.0-r1_all.ipk`

Override `GO`, `WIFI_GUARD_SDK_CACHE` or `JOBS` if necessary. An extracted private host dependency tree may be supplied through `WIFI_GUARD_HOST_DEPS` without system installation. Do not build release artifacts from an unchecked working tree; run the checks first.

## Install on the actual approved router

Check flash/RAM, verify the target/architecture and installed dependencies first. Do not assume a default LAN address. Back up any existing registry and config privately; never overwrite unrelated files. Keep at least several MiB of spare flash beyond the binary/package footprint.

```sh
export ROUTER='root@YOUR_ROUTER_ADDRESS'
ssh "$ROUTER" 'df -k /overlay; free; opkg print-architecture'
scp -O build/packages/wifi-guard_0.3.0-r1_aarch64_cortex-a53.ipk build/packages/luci-app-wifi-guard_0.3.0-r1_all.ipk "$ROUTER":/tmp/
ssh "$ROUTER" 'opkg install /tmp/wifi-guard_0.3.0-r1_aarch64_cortex-a53.ipk /tmp/luci-app-wifi-guard_0.3.0-r1_all.ipk'
```

OpenWrt's default package postinstall enables/starts the package's own procd service. Telegram defaults to disabled, so baseline import does not send messages. No network/firewall/DNS/Wi-Fi settings are changed or services reloaded. State remains `/etc/wifi-guard/state.json`; socket `/var/run/wifi-guard/control.sock`; token file `/etc/config/wifi-guard`. The state directory is 0700 and secret config/state files 0600.

Register the new rpcd executable using an rpcd reload during an approved management window; this is a management daemon reload, not a network/Wi-Fi reload:

```sh
ssh "$ROUTER" '/etc/init.d/rpcd reload'
```

Refresh/re-login to LuCI, then open Services → WiFi Guard. A LuCI cache may need refreshing after installing a new menu. Do not restart uHTTPd/networking just to troubleshoot a cached page. A missing daemon or invalid config is displayed as unavailable, not as an empty successful result.

## Configure and verify Telegram

1. Start a private conversation with your bot first; bots cannot initiate a DM to a user who has not started them.
2. In authenticated LuCI, enter your numeric chat ID and bot token, enable Telegram, then save. The password field stays blank on subsequent reads; its placeholder reports only whether a token exists.
3. Click **Send test message**, and verify the explicitly labeled message in the target Telegram chat. The UI reports API acceptance, not proof that a human read it.
4. For a new-device test, use a genuinely new Wi-Fi client or an isolated test-registry copy as described in the project verification notes. Do not reset production identity state or change phone privacy settings without a specific reason.

A CLI test is also available after secure provisioning; no token is accepted in flags:

```sh
ssh "$ROUTER" '/usr/sbin/wifi-guard -test-telegram'
```

Changing settings through the page updates only this application's configuration and live notifier; it never reloads the network. External manual UCI changes require reloading only `wifi-guard`. Root/admin access to generic UCI APIs remains privileged; the application's own read methods never return token bytes.

## Registry ownership

The daemon exclusively locks `state.json.lock` before loading mutable state. A second instance with the same registry exits with `registry is already in use`, including when RPC is disabled. Read-only `-dump` remains available. The adjacent lock file contains no credentials and may remain after shutdown; the kernel releases its lock on process exit/crash/reboot. Do not delete or replace it while a daemon is running. Stop only WiFi Guard before an intentional state migration.

## Upgrade and removal

`/etc/config/wifi-guard` is an opkg conffile. The dynamically created registry is not shipped as an empty package file and is not overwritten by upgrade. Back up both before an upgrade; do not use force-maintainer options that discard local configuration.

```sh
# Upgrade using verified new package filenames; only the monitor may restart.
# To stop or reload just this application:
/etc/init.d/wifi-guard stop
/etc/init.d/wifi-guard reload

# Remove application packages (review paths; keep the registry unless intentional):
opkg remove luci-app-wifi-guard wifi-guard
```

Package removal can leave modified config and unowned registry data behind; this deliberately preserves known identities and secrets until the operator explicitly removes them. Disable/stop the service before manual file deletion. LuCI/rpcd may need a management-only refresh to remove a stale menu/object. No uninstall command should reset wireless, firewall, DHCP or DNS.
