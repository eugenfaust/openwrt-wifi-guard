#!/usr/bin/env bash
# Build real OpenWrt 24.10.2 .ipk files with the official SDK (Linux x86-64).
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
CACHE=${WIFI_GUARD_SDK_CACHE:-"$HOME/.local/share/wifi-guard-sdk"}
GO=${GO:-go}
NAME=openwrt-sdk-24.10.2-mediatek-filogic_gcc-13.3.0_musl.Linux-x86_64
SHA=df288284baa46d37cbc71812130b72617333f886f5c93c11f0548e28f0bb8309
URL=https://downloads.openwrt.org/releases/24.10.2/targets/mediatek/filogic
if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
    printf 'This SDK requires Linux x86-64; ordinary Go builds also work on macOS.\n' >&2
    exit 1
fi
for tool in curl sha256sum tar zstd make gcc python3; do command -v "$tool" >/dev/null; done
command -v "$GO" >/dev/null
mkdir -p "$CACHE" "$ROOT/build/packages"
ARCHIVE="$CACHE/$NAME.tar.zst"
if ! printf '%s  %s\n' "$SHA" "$ARCHIVE" | sha256sum --status -c - 2>/dev/null; then
    curl -fLsS --retry 2 -C - "$URL/$NAME.tar.zst" -o "$ARCHIVE"
fi
printf '%s  %s\n' "$SHA" "$ARCHIVE" | sha256sum -c -
SDK="$CACHE/$NAME"
if [[ ! -d "$SDK/staging_dir" ]]; then tar --zstd -xf "$ARCHIVE" -C "$CACHE"; fi
for package in wifi-guard luci-app-wifi-guard; do
    LINK="$SDK/package/$package"
    TARGET="$ROOT/packaging/openwrt/$package"
    if [[ -e "$LINK" || -L "$LINK" ]]; then
        [[ $(readlink -f "$LINK") == "$TARGET" ]] || { printf 'SDK package path already occupied: %s\n' "$LINK" >&2; exit 1; }
    else
        ln -s "$TARGET" "$LINK"
    fi
done
# Normal Linux hosts need libncurses-dev. A private extracted dependency tree is
# also supported without modifying system packages (CPATH survives SDK checks).
if [[ -n ${WIFI_GUARD_HOST_DEPS:-} ]]; then
    export CPATH="$WIFI_GUARD_HOST_DEPS/usr/include${CPATH:+:$CPATH}"
    export LIBRARY_PATH="$WIFI_GUARD_HOST_DEPS/usr/lib/x86_64-linux-gnu${LIBRARY_PATH:+:$LIBRARY_PATH}"
fi
make -C "$SDK" defconfig
make -C "$SDK" package/wifi-guard/clean package/luci-app-wifi-guard/clean
make -C "$SDK" -j"${JOBS:-2}" package/wifi-guard/compile package/luci-app-wifi-guard/compile \
    V=s WIFI_GUARD_SOURCE_DIR="$ROOT" WIFI_GUARD_GO="$(command -v "$GO")"
# SDK packaging, not handcrafted/renamed tar archives. Verify metadata separately.
cp "$SDK/bin/packages/aarch64_cortex-a53/base/wifi-guard_0.2.0-r2_aarch64_cortex-a53.ipk" "$ROOT/build/packages/"
cp "$SDK/bin/packages/aarch64_cortex-a53/base/luci-app-wifi-guard_0.2.0-r2_all.ipk" "$ROOT/build/packages/"
sha256sum "$ROOT/build/packages/wifi-guard_0.2.0-r2_aarch64_cortex-a53.ipk" "$ROOT/build/packages/luci-app-wifi-guard_0.2.0-r2_all.ipk"
