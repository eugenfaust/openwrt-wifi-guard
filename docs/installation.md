# Installation and package lifecycle

## Supported packages and release status

The supported package target is **OpenWrt 24.10, mediatek/filogic, `aarch64_cortex-a53`**, using IPK/opkg. The official build SDK is OpenWrt 24.10.2. These packages are not universal OpenWrt packages: other targets, architectures and APK-based firmware are not supported. The LuCI package's `all` architecture does not make the daemon portable to other targets.

The upcoming **0.3.0-r5** adds automatic rpcd/LuCI refresh for installation through LuCI alone. **This new installation behavior has not yet been live-tested on hardware.** Existing router verification applies to 0.3.0-r4 and earlier, not r5; see [verification](verification.md).

## Install through LuCI — no SSH required

1. Download both matching artifacts from the [r5 preview release](https://github.com/eugenfaust/openwrt-wifi-guard/releases/tag/v0.3.0-r5) to your computer:
   - `wifi-guard_0.3.0-r5_aarch64_cortex-a53.ipk`
   - `luci-app-wifi-guard_0.3.0-r5_all.ipk`
   Use the same version/release for both packages. Building them yourself is optional; see the developer section below.
2. Log in to your actual router's LuCI address. Confirm the firmware/target under **Status → Overview**. Under **System → Software**, check available storage; also check free memory in Status. Keep at least several MiB of spare flash beyond the package footprint. Do not install if the target or available space is unsuitable.
3. If upgrading, first make a configuration backup under **System → Backup / Flash Firmware**. Also have an advanced administrator privately back up `/etc/config/wifi-guard` and the complete `/etc/wifi-guard/` directory, including the device registry and any policy journal. Do not assume the standard LuCI backup includes dynamically created private state. These backups contain secrets/device identifiers; do not publish them. This advanced backup step is separate from the normal installation path.
4. In **System → Software**, click **Update lists**. The router needs Internet access and working matching firmware feeds to fetch runtime dependencies. If list updates or dependency resolution fail, fix that first; never use force-dependency options or force installation on unsupported firmware.
5. Click **Upload Package**, select `wifi-guard_0.3.0-r5_aarch64_cortex-a53.ipk`, and complete installation. Wait for success before continuing.
6. Use **Upload Package** again to install `luci-app-wifi-guard_0.3.0-r5_all.ipk`.
7. Refresh the browser; log in again if the session expires. Open **Services → WiFi Guard** (Overview, Devices, History, Settings), then configure Telegram as below.

No SSH commands, manual service start or manual rpcd reload are needed for this normal installation path. OpenWrt's default package postinstall enables/starts only the `wifi-guard` procd service. The r5 LuCI package postinstall automatically refreshes rpcd and LuCI caches to register the plugin/menu; a browser refresh or new login may still be needed. This is a management-only refresh: network, Wi-Fi/hostapd, firewall, DHCP and DNS services are untouched. Do not restart uHTTPd or networking merely to refresh the menu.

Telegram defaults to disabled and initial import is silent. State remains `/etc/wifi-guard/state.json`, the private socket `/var/run/wifi-guard/control.sock`, and secret UCI settings `/etc/config/wifi-guard`. The state directory is 0700 and secret config/state files 0600. A missing daemon or invalid config appears as unavailable, not as an empty successful result.

## Configure and verify Telegram

1. Start a private conversation with your bot first; bots cannot initiate a DM to a user who has not started them.
2. In authenticated **Services → WiFi Guard → Settings**, enter your numeric chat ID and bot token, enable Telegram, then save. The password field stays blank on subsequent reads; blank preserves the saved token, explicit clear removes it.
3. Click **Send test message**, and verify the explicitly labeled message in the target Telegram chat. The UI reports API acceptance, not proof that a human read it.
4. For a new-device test, use a genuinely new Wi-Fi client or an isolated test-registry copy as described in the project verification notes. Do not reset production identity state or change phone privacy settings without a specific reason.

Enter credentials only in the authenticated settings page; never publish them or put them in process arguments. Use HTTPS or a trusted management connection: HTTP does not encrypt the token. Changing settings through the page updates only this application's configuration and live notifier; it never reloads the network. The application's read methods never return token bytes.

## Upgrade and removal through LuCI

Upgrade using the same two-package order and matching release versions above, after private backups. `/etc/config/wifi-guard` is an opkg conffile. The dynamically created registry is not shipped as an empty package file and is not overwritten by upgrade. Do not force replacement of local configuration; opkg may leave an `-opkg` sidecar while preserving the active file. The monitor may restart and the LuCI package refreshes management state, not network services.

To remove, stop/disable only WiFi Guard under **System → Startup**, then remove `luci-app-wifi-guard` followed by `wifi-guard` under **System → Software**. Modified config and unowned registry data may remain deliberately to preserve identities/secrets. Delete them only intentionally after backup. Refresh/re-login if a removed menu remains cached. No removal step should reset wireless, firewall, DHCP or DNS.

## Advanced administrator options — optional SSH

These commands are not part of the normal LuCI install. Use the actual approved router address, never an assumed default. Advanced administrators can create private backups of the configuration and complete state directory before upgrades; retain them outside the repository and verify their contents privately. External manual UCI changes require reloading only `wifi-guard`.

```sh
export ROUTER='root@YOUR_ROUTER_ADDRESS'
# Optional command-line install, after compatibility/space checks and private backup:
ssh "$ROUTER" 'opkg update'
scp -O build/packages/wifi-guard_0.3.0-r5_aarch64_cortex-a53.ipk build/packages/luci-app-wifi-guard_0.3.0-r5_all.ipk "$ROUTER":/tmp/
ssh "$ROUTER" 'opkg install /tmp/wifi-guard_0.3.0-r5_aarch64_cortex-a53.ipk && opkg install /tmp/luci-app-wifi-guard_0.3.0-r5_all.ipk'
# Optional test using already securely provisioned credentials, never token flags:
ssh "$ROUTER" '/usr/sbin/wifi-guard -test-telegram'
```

For an unexpectedly missing menu/object, first refresh/re-login and inspect installation errors. An administrator may inspect `ubus -v list wifi-guard` and reload only rpcd as a fallback, not a required r5 installation step. Do not reload network/Wi-Fi services to fix a management cache.

### Registry ownership

The daemon exclusively locks `state.json.lock` before loading mutable state. A second instance with the same registry exits with `registry is already in use`, including when RPC is disabled. Read-only `-dump` remains available. The adjacent lock file contains no credentials and may remain after shutdown; the kernel releases its lock on process exit/crash/reboot. Do not delete or replace it while a daemon is running. Stop only WiFi Guard before an intentional state migration.

## Developer build — optional

Go 1.23+ is sufficient for the stdlib-only daemon. Node.js 20+ and Python 3 are development-test tools only; neither is needed on the router.

```sh
make check smoke arm64
# Linux x86-64 development-host dependencies, not router packages:
# Debian/Ubuntu: build-essential libncurses-dev zstd python3 rsync gawk curl
make packages
```

The script downloads/resumes the official OpenWrt 24.10.2 SDK in `$HOME/.local/share/wifi-guard-sdk`, checks its pinned SHA-256 and invokes its real package build system. The explicit host Go toolchain compiles static `linux/arm64` with CGO disabled; no C ABI/toolchain dependency is introduced by the daemon. `WIFI_GUARD_SOURCE_DIR` identifies the local source; VCS stamping is disabled in the SDK source copy. This is not handcrafted IPK archive generation. Missing feed metadata warnings do not remove the router's runtime dependency requirements.

Outputs for the upcoming release:
- `build/packages/wifi-guard_0.3.0-r5_aarch64_cortex-a53.ipk`
- `build/packages/luci-app-wifi-guard_0.3.0-r5_all.ipk`

Override `GO`, `WIFI_GUARD_SDK_CACHE` or `JOBS` if necessary. An extracted private host dependency tree may be supplied through `WIFI_GUARD_HOST_DEPS` without system installation. Do not build release artifacts from an unchecked working tree; run the checks first. A successful local build/test does not verify r5 installation on hardware.
