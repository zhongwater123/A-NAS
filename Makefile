GO ?= go
NPM ?= npm
BUILD_DIR ?= build
VERSION ?= dev

.PHONY: all web-install web-typecheck web-test web-build fmt fmt-check docs-check ops-check vet test build build-binaries check clean

all: check

web-install:
	cd web && $(NPM) ci --no-audit --no-fund

web-typecheck: web-install
	cd web && $(NPM) run typecheck

web-test: web-install
	cd web && $(NPM) test

web-build: web-install
	cd web && $(NPM) run build

fmt:
	gofmt -w $$(find . -name '*.go' -type f -not -path './.git/*')

fmt-check:
	@files="$$(gofmt -l $$(find . -name '*.go' -type f -not -path './.git/*'))"; \
	if [ -n "$$files" ]; then echo "The following files need gofmt:"; echo "$$files"; exit 1; fi

docs-check:
	$(GO) run ./tools/doccheck

ops-check:
	shellcheck scripts/remote-activate-release.sh scripts/run-kiosk.sh
	bash -n scripts/remote-activate-release.sh scripts/run-kiosk.sh

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

build: web-build build-binaries

build-binaries:
	mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BUILD_DIR)/anas-api ./cmd/anas-api
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BUILD_DIR)/anas-host-agent ./cmd/anas-host-agent

check: web-typecheck web-test web-build fmt-check docs-check ops-check vet test build-binaries

clean:
	rm -f $(BUILD_DIR)/anas-api $(BUILD_DIR)/anas-host-agent
	find internal/webui/dist -mindepth 1 ! -name '.keep' -delete
