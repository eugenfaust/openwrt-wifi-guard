GO ?= go
.PHONY: test check build arm64 smoke packages
check:
	test -z "$$($(GO) fmt ./...)"
	$(GO) vet ./...
	$(GO) test -race ./...
	node tests/luci.test.cjs
	sh -n packaging/openwrt/wifi-guard/files/wifi-guard.init
	sh -n luci/root/usr/libexec/rpcd/wifi-guard
test:
	$(GO) test ./...
build:
	mkdir -p build
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o build/wifi-guard ./cmd/wifi-guard
arm64:
	mkdir -p build
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOARM64=v8.0 $(GO) build -trimpath -ldflags='-s -w' -o build/wifi-guard-linux-arm64 ./cmd/wifi-guard
smoke: build
	python3 tests/smoke.py build/wifi-guard
packages:
	GO=$(GO) bash scripts/build-openwrt.sh
