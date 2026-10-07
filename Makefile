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
	shellcheck scripts/remote-activate-release.sh scripts/run-kiosk.sh scripts/install-v1.0.1-system-services.sh scripts/provision-v1.0.1-rc.sh
	bash -n scripts/remote-activate-release.sh scripts/run-kiosk.sh scripts/install-v1.0.1-system-services.sh scripts/provision-v1.0.1-rc.sh
	env ANAS_KIOSK_OUTPUT=DP-2 ANAS_KIOSK_TRANSFORM=90 ANAS_KIOSK_SCALE=1.5 bash scripts/run-kiosk.sh --check-output-config
	! env ANAS_KIOSK_OUTPUT=DP-2 ANAS_KIOSK_TRANSFORM=sideways ANAS_KIOSK_SCALE=1.5 bash scripts/run-kiosk.sh --check-output-config
	grep -Fqx 'ConditionPathExists=/home/anas-dev/apps/a-nas/current/kiosk-launcher' deploy/systemd/system/anas-kiosk@.service
	grep -Fqx 'ExecStart=/usr/bin/cage -s -- /home/anas-dev/apps/a-nas/current/kiosk-launcher' deploy/systemd/system/anas-kiosk@.service
	grep -Fqx 'EnvironmentFile=-/etc/a-nas/kiosk.env' deploy/systemd/system/anas-kiosk@.service
	grep -Fqx 'User=root' deploy/systemd/system/anas-host-agent.service
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-host-agent' deploy/systemd/system/anas-host-agent.service
	grep -Fqx 'User=a-nas' deploy/systemd/system/anas-api.service
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-api' deploy/systemd/system/anas-api.service
	grep -Fq 'ANAS_HOST_AGENT_GROUP=a-nas' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'EXPECTED_DISK_WWN' scripts/provision-v1.0.1-rc.sh

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
