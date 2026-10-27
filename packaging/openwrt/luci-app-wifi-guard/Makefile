# SPDX-License-Identifier: MIT
WIFI_GUARD_SOURCE_DIR ?= $(realpath $(CURDIR)/../../..)
include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-wifi-guard
PKG_VERSION:=0.2.0
PKG_RELEASE:=2
PKG_LICENSE:=MIT
PKGARCH:=all

include $(INCLUDE_DIR)/package.mk

define Package/luci-app-wifi-guard
  PKGARCH:=all
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI interface for WiFi Guard
  DEPENDS:=+wifi-guard +luci-base +rpcd
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Configure
endef

define Build/Compile
endef

define Package/luci-app-wifi-guard/install
	$(INSTALL_DIR) $(1)/www/luci-static/resources/view/wifi-guard
	$(INSTALL_DATA) $(WIFI_GUARD_SOURCE_DIR)/luci/htdocs/luci-static/resources/view/wifi-guard/overview.js $(1)/www/luci-static/resources/view/wifi-guard/overview.js
	$(INSTALL_DIR) $(1)/usr/libexec/rpcd $(1)/usr/share/rpcd/acl.d $(1)/usr/share/luci/menu.d
	$(INSTALL_BIN) $(WIFI_GUARD_SOURCE_DIR)/luci/root/usr/libexec/rpcd/wifi-guard $(1)/usr/libexec/rpcd/wifi-guard
	$(INSTALL_DATA) $(WIFI_GUARD_SOURCE_DIR)/luci/root/usr/share/rpcd/acl.d/luci-app-wifi-guard.json $(1)/usr/share/rpcd/acl.d/luci-app-wifi-guard.json
	$(INSTALL_DATA) $(WIFI_GUARD_SOURCE_DIR)/luci/root/usr/share/luci/menu.d/luci-app-wifi-guard.json $(1)/usr/share/luci/menu.d/luci-app-wifi-guard.json
endef

define Package/luci-app-wifi-guard/postinst
#!/bin/sh
# Explicit operator start/refresh avoids implicit service restarts during install.
[ -n "$$IPKG_INSTROOT" ] || rm -f /tmp/luci-indexcache /tmp/luci-indexcache.*
exit 0
endef

$(eval $(call BuildPackage,luci-app-wifi-guard))
