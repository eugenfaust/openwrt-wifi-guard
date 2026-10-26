GO ?= go
.PHONY: test check build arm64 smoke
check:
	test -z "$$($(GO) fmt ./...)"
	$(GO) vet ./...
	$(GO) test -race ./...
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
