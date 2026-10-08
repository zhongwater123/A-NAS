GO ?= go
NPM ?= npm
BUILD_DIR ?= build
VERSION ?= dev
WEB_DEPS_STAMP ?= web/node_modules/.package-lock.json
VALIDATION_MANIFEST ?= $(BUILD_DIR)/.validated-build

.PHONY: all web-install web-typecheck web-test web-build fmt fmt-check docs-check ops-check vet test root-integration-test build build-binaries check check-steps clean

all: check

web-install: $(WEB_DEPS_STAMP)

$(WEB_DEPS_STAMP): web/package.json web/package-lock.json
	cd web && $(NPM) ci --no-audit --no-fund
	@test -f $(WEB_DEPS_STAMP)

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
	shellcheck scripts/remote-activate-release.sh scripts/run-kiosk.sh scripts/install-v1.0.1-system-services.sh scripts/provision-v1.0.1-rc.sh scripts/smoke-photo-service.sh
	bash -n scripts/remote-activate-release.sh scripts/run-kiosk.sh scripts/install-v1.0.1-system-services.sh scripts/provision-v1.0.1-rc.sh
	env ANAS_KIOSK_OUTPUT=DP-2 ANAS_KIOSK_TRANSFORM=90 ANAS_KIOSK_SCALE=1.5 bash scripts/run-kiosk.sh --check-output-config
	! env ANAS_KIOSK_OUTPUT=DP-2 ANAS_KIOSK_TRANSFORM=sideways ANAS_KIOSK_SCALE=1.5 bash scripts/run-kiosk.sh --check-output-config
	grep -Fqx 'ConditionPathExists=/home/anas-dev/apps/a-nas/current/kiosk-launcher' deploy/systemd/system/anas-kiosk@.service
	grep -Fqx 'ExecStart=/usr/bin/cage -s -- /home/anas-dev/apps/a-nas/current/kiosk-launcher' deploy/systemd/system/anas-kiosk@.service
	grep -Fqx 'EnvironmentFile=-/etc/a-nas/kiosk.env' deploy/systemd/system/anas-kiosk@.service
	grep -Fqx 'ReadWritePaths=%h/.config/a-nas/state' deploy/systemd/user/anas-api.service
	grep -Fq 'install -d -m 0700 "$$config_dir/state"' scripts/remote-activate-release.sh
	grep -Fq 'stage-only' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fqx 'User=root' deploy/systemd/system/anas-host-agent.service
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-host-agent' deploy/systemd/system/anas-host-agent.service
	grep -Fqx 'ReadWritePaths=/etc /var/lib/a-nas /var/lib/samba /srv/a-nas /run/a-nas /run/samba' deploy/systemd/system/anas-host-agent.service
	grep -Fqx 'User=a-nas' deploy/systemd/system/anas-api.service
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-api' deploy/systemd/system/anas-api.service
	grep -Fq 'ANAS_HOST_AGENT_GROUP=a-nas' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'ANAS_FILE_BROKER_SOCKET=/run/a-nas/file-broker.sock' scripts/install-v1.0.1-system-services.sh
	grep -Fq '[[ -S /run/a-nas-container/agent.sock ]]' scripts/install-v1.0.1-system-services.sh
	grep -Fq "printf 'ANAS_CONTAINERS_MODE=agent\\n' >> /etc/a-nas/anas-api.env" scripts/install-v1.0.1-system-services.sh
	grep -Fqx 'ReadWritePaths=/var/lib/a-nas' deploy/systemd/system/anas-api.service
	grep -Fq 'setfacl getfacl' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'acl btrfs-progs' scripts/provision-v1.0.1-rc.sh
	jq -e '.PasswordManagerEnabled == false and .PasswordManagerPasskeysEnabled == false and .SyncDisabled == true' deploy/chromium/policies/managed/a-nas.json >/dev/null
	grep -Fq '/etc/chromium/policies/managed/a-nas.json' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'a-nas-chromium-policy.json.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq 'ANAS_SCREENSAVER_DIRECTORY' cmd/anas-api/main.go scripts/remote-activate-release.sh scripts/install-v1.0.1-system-services.sh
	grep -Fq 'screensaver_hash_list' scripts/remote-activate-release.sh
	grep -Fq 'screensaver_hash_count' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'EXPECTED_DISK_WWN' scripts/provision-v1.0.1-rc.sh
	! grep -Eq 'setup.?code|setup_code' scripts/install-v1.0.1-system-services.sh scripts/provision-v1.0.1-rc.sh
	grep -Fqx 'User=anas-container' deploy/systemd/system/anas-container-agent.service
	grep -Fqx 'SupplementaryGroups=docker' deploy/systemd/system/anas-container-agent.service
	grep -Fqx 'ExecStart=/usr/local/lib/a-nas/anas-container-agent' deploy/systemd/system/anas-container-agent.service
	grep -Fqx 'RuntimeDirectoryMode=0750' deploy/systemd/system/anas-container-agent.service
	grep -Fqx 'User=a-nas-photos' deploy/systemd/system/anas-photos.service
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-api photo-service' deploy/systemd/system/anas-photos.service
	grep -Fqx 'ReadWritePaths=/srv/a-nas/data' deploy/systemd/system/anas-photos.service
	grep -Fqx 'RuntimeDirectory=a-nas-photos' deploy/systemd/system/anas-photos.service
	grep -Fqx 'PrivateNetwork=true' deploy/systemd/system/anas-photos.service
	grep -Fqx 'SupplementaryGroups=a-nas-photos' deploy/systemd/system/anas-api.service
	grep -Fqx 'RuntimeDirectory=a-nas a-nas-sessions' deploy/systemd/system/anas-host-agent.service
	grep -Fq 'ANAS_PHOTO_SESSION_GROUP=a-nas-photos' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'anas-photos-system.service.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

# Modifies accounts, groups, and Samba state: disposable privileged container only.
root-integration-test:
	ANAS_ROOT_INTEGRATION=1 $(GO) test -tags rootintegration -count=1 ./internal/hostops/linux

build: web-build build-binaries

build-binaries: web-build
	mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BUILD_DIR)/anas-api ./cmd/anas-api
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BUILD_DIR)/anas-host-agent ./cmd/anas-host-agent
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BUILD_DIR)/anas-container-agent ./cmd/anas-container-agent

check:
	@rm -f $(VALIDATION_MANIFEST)
	@$(MAKE) --no-print-directory check-steps VERSION="$(VERSION)"
	@commit="$$(git rev-parse --short=12 HEAD)"; \
	api_sha="$$(sha256sum $(BUILD_DIR)/anas-api | cut -d ' ' -f 1)"; \
	agent_sha="$$(sha256sum $(BUILD_DIR)/anas-host-agent | cut -d ' ' -f 1)"; \
	printf 'commit=%s\nproduct_version=%s\napi_sha256=%s\nhost_agent_sha256=%s\n' \
		"$$commit" "$(VERSION)" "$$api_sha" "$$agent_sha" > $(VALIDATION_MANIFEST)

check-steps: web-typecheck web-test web-build fmt-check docs-check ops-check vet test build-binaries

clean:
	rm -f $(BUILD_DIR)/anas-api $(BUILD_DIR)/anas-host-agent $(BUILD_DIR)/anas-container-agent $(VALIDATION_MANIFEST)
	find internal/webui/dist -mindepth 1 ! -name '.keep' -delete
