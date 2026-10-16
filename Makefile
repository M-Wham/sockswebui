# sockswebui build.
# VERSION defaults to the HEAD short SHA (no tag, no -dirty); override with `make VERSION=x`.

VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BIN     := bin/sockswebui
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test ui release release-check clean rss-check pkg-test

all: build

ui:
	cd web && npm ci && npm run build

build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/sockswebui

release: ui
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/sockswebui-linux-amd64 ./cmd/sockswebui
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/sockswebui-linux-arm64 ./cmd/sockswebui
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/sockswebui-linux-armv7 ./cmd/sockswebui
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -trimpath -ldflags "$(LDFLAGS)" -o dist/sockswebui-linux-mipsle ./cmd/sockswebui
	CGO_ENABLED=0 GOOS=linux GOARCH=mips GOMIPS=softfloat go build -trimpath -ldflags "$(LDFLAGS)" -o dist/sockswebui-linux-mips ./cmd/sockswebui
	sh packaging/build-deb.sh $(VERSION) dist
	cd dist && sha256sum sockswebui-linux-amd64 sockswebui-linux-arm64 sockswebui-linux-armv7 sockswebui-linux-mipsle sockswebui-linux-mips sockswebui_*.deb install.sh sockswebui.service sockswebui.init > SHA256SUMS

test:
	go test ./...
	cd web && npm ci && npm test

pkg-test:
	bash scripts/pkg-test.sh all

rss-check:
	bash scripts/rss-check.sh

release-check:
	bash scripts/release-check.sh $(VERSION)

clean:
	rm -rf bin
	rm -f dist/sockswebui-linux-* dist/sockswebui_*.deb dist/SHA256SUMS
	find web/dist -mindepth 1 -maxdepth 1 ! -name .keep -exec rm -rf {} +
	mkdir -p web/dist
	touch web/dist/.keep
