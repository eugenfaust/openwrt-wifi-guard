# SPDX-License-Identifier: MIT
# Official OpenWrt SDK packaging; Go is an explicit host tool, CGO stays disabled.
WIFI_GUARD_SOURCE_DIR ?= $(realpath $(CURDIR)/../../..)
WIFI_GUARD_GO ?= go
include $(TOPDIR)/rules.mk

PKG_NAME:=wifi-guard
PKG_VERSION:=0.3.0
PKG_RELEASE:=6
PKG_LICENSE:=MIT
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)
PKG_BUILD_PARALLEL:=1

include $(INCLUDE_DIR)/package.mk

define Package/wifi-guard
  SOURCE:=packaging/openwrt/wifi-guard
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Authorized Wi-Fi client monitor with Telegram alerts
  DEPENDS:=@aarch64 +ubus +uci +ca-bundle
endef

define Package/wifi-guard/description
  Router-native Wi-Fi device registry. No Ethernet monitoring or automatic blocking.
  Static Go ARM64 daemon with a root-private local RPC bridge.
endef

define Package/wifi-guard/conffiles
/etc/config/wifi-guard
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) $(WIFI_GUARD_SOURCE_DIR)/go.mod $(WIFI_GUARD_SOURCE_DIR)/cmd $(WIFI_GUARD_SOURCE_DIR)/internal $(PKG_BUILD_DIR)/
endef

define Build/Configure
endef

define Build/Compile
	cd $(PKG_BUILD_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOARM64=v8.0 GOTOOLCHAIN=local $(WIFI_GUARD_GO) build -buildvcs=false -trimpath -ldflags='-s -w' -o wifi-guard ./cmd/wifi-guard
endef

define Package/wifi-guard/install
	$(INSTALL_DIR) $(1)/usr/sbin $(1)/etc/init.d $(1)/etc/config $(1)/etc/wifi-guard
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/wifi-guard $(1)/usr/sbin/wifi-guard
	$(INSTALL_BIN) ./files/wifi-guard.init $(1)/etc/init.d/wifi-guard
	$(INSTALL_CONF) ./files/wifi-guard.config $(1)/etc/config/wifi-guard
	chmod 700 $(1)/etc/wifi-guard
endef

define Package/wifi-guard/postinst
#!/bin/sh
[ -n "$$IPKG_INSTROOT" ] || chmod 600 /etc/config/wifi-guard
exit 0
endef

$(eval $(call BuildPackage,wifi-guard))
