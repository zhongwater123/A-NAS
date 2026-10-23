GO ?= go
SYSTEM_TEST_IMAGE ?= anas-systemd:trixie
# ShellCheck releases judge differently (0.11 made SC2002 optional), so ops-check
# runs only this one; CI installs it from the release by hash.
SHELLCHECK_VERSION = 0.11.0
# Every tracked shell script; in a copy without .git, every *.sh file.
SHELL_SCRIPTS = $(shell git ls-files '*.sh' 2>/dev/null || find . -name '*.sh' -not -path './.git/*' -not -path './.claude/worktrees/*' -not -path '*/node_modules/*' | sed 's|^\./||' | sort)
NPM ?= npm
RUFF ?= ruff
BUILD_DIR ?= build
VERSION ?= dev
WEB_DEPS_STAMP ?= web/node_modules/.package-lock.json
VALIDATION_MANIFEST ?= $(BUILD_DIR)/.validated-build
# golangci-lint has a module of its own: in go.mod its 200-odd dependencies
# would raise versions the product builds with (logrus, jsonschema and others).
GOLANGCI_LINT ?= $(GO) tool -modfile=tools/golangci-lint/go.mod golangci-lint
# Packages tested at once under the race detector, as on a 4-core CI runner.
RACE_PARALLEL ?= 4

.PHONY: all web-install web-typecheck web-lint web-test web-build fmt fmt-check docs-check ops-check vet go-lint vuln-check test race-test ai-lint ai-test root-integration-test system-test build build-binaries check check-steps clean

all: check

web-install: $(WEB_DEPS_STAMP)

$(WEB_DEPS_STAMP): web/package.json web/package-lock.json
	cd web && $(NPM) ci --no-audit --no-fund
	@test -f $(WEB_DEPS_STAMP)

web-typecheck: web-install
	cd web && $(NPM) run typecheck

# Oxlint with React's hooks rules, then Biome's formatting check.
web-lint: web-install
	cd web && $(NPM) run lint

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

# Shell scripts, units and installer conventions, and Dependabot's groups
# (tools/depcheck keeps 0.x web packages out of them).
ops-check:
	@have="$$(shellcheck --version 2>/dev/null | sed -n 's/^version: //p')"; \
	if [ "$$have" != "$(SHELLCHECK_VERSION)" ]; then echo "ops-check needs ShellCheck $(SHELLCHECK_VERSION); found: $${have:-none}" >&2; exit 1; fi
	shellcheck $(SHELL_SCRIPTS)
	@for script in $(SHELL_SCRIPTS); do bash -n "$$script" || exit 1; done
	$(GO) run ./tools/depcheck
	env ANAS_KIOSK_OUTPUT=DP-2 ANAS_KIOSK_TRANSFORM=90 ANAS_KIOSK_SCALE=1.5 bash scripts/run-kiosk.sh --check-output-config
	! env ANAS_KIOSK_OUTPUT=DP-2 ANAS_KIOSK_TRANSFORM=sideways ANAS_KIOSK_SCALE=1.5 bash scripts/run-kiosk.sh --check-output-config
	grep -Fqx 'ConditionPathExists=/home/anas-dev/apps/a-nas/current/kiosk-launcher' deploy/systemd/system/anas-kiosk@.service
	grep -Fqx 'ExecStart=/usr/bin/cage -s -- /home/anas-dev/apps/a-nas/current/kiosk-launcher' deploy/systemd/system/anas-kiosk@.service
	grep -Fqx 'EnvironmentFile=-/etc/a-nas/kiosk.env' deploy/systemd/system/anas-kiosk@.service
	grep -Fqx 'ReadWritePaths=%h/.config/a-nas/state' deploy/systemd/user/anas-api.service
	grep -Fq 'install -d -m 0700 "$$config_dir/state"' scripts/remote-activate-release.sh
	grep -Fq 'stage-only' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-host-agent' deploy/systemd/system/anas-host-agent.service
	grep -Fqx 'ReadWritePaths=/etc /var/lib/a-nas /var/lib/samba /srv/a-nas /run/a-nas /run/samba /sys/devices' deploy/systemd/system/anas-host-agent.service
	grep -Fqx 'User=a-nas' deploy/systemd/system/anas-api.service
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-api' deploy/systemd/system/anas-api.service
	grep -Fq 'ANAS_HOST_AGENT_GROUP=a-nas' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'ANAS_FILE_BROKER_SOCKET=/run/a-nas/file-broker.sock' scripts/install-v1.0.1-system-services.sh
	grep -Fq '[[ -S /run/a-nas-container/agent.sock ]]' scripts/install-v1.0.1-system-services.sh
	grep -Fq "printf 'ANAS_CONTAINERS_MODE=agent\\n' >> /etc/a-nas/anas-api.env" scripts/install-v1.0.1-system-services.sh
	grep -Fqx 'ReadWritePaths=/var/lib/a-nas /run/a-nas-share-files' deploy/systemd/system/anas-api.service
	grep -Fq 'setfacl getfacl' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'acl btrfs-progs' scripts/provision-v1.0.1-rc.sh
	jq -e '.PasswordManagerEnabled == false and .PasswordManagerPasskeysEnabled == false and .SyncDisabled == true' deploy/chromium/policies/managed/a-nas.json >/dev/null
	grep -Fq '/etc/chromium/policies/managed/a-nas.json' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'a-nas-chromium-policy.json.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq "'ANAS_SCREENSAVER_DIRECTORY=/var/lib/a-nas/screensavers/current'" scripts/install-v1.0.1-system-services.sh
	grep -Fq '"/var/lib/a-nas/screensavers/current"' cmd/anas-api/config.go
	grep -Fq 'install-screensavers.sh.incoming' scripts/deploy-dev.ps1
	grep -Fq 'install-screensavers.sh.incoming' scripts/remote-activate-release.sh
	grep -Fq '"$$source_release/install-screensavers.sh" "$$temporary/install-screensavers.sh"' scripts/install-v1.0.1-system-services.sh
	! grep -Eq 'screensaver_[0-9a-z]*_sha256|ScreensaverVideo' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh scripts/install-v1.0.1-system-services.sh
	grep -Fq 'EXPECTED_DISK_WWN' scripts/provision-v1.0.1-rc.sh
	! grep -Eq 'setup.?code|setup_code' scripts/install-v1.0.1-system-services.sh scripts/provision-v1.0.1-rc.sh
	grep -Fqx 'User=anas-container' deploy/systemd/system/anas-container-agent.service
	grep -Fqx 'SupplementaryGroups=docker' deploy/systemd/system/anas-container-agent.service
	grep -Fqx 'ExecStart=/usr/local/lib/a-nas/anas-container-agent' deploy/systemd/system/anas-container-agent.service
	grep -Fqx 'RuntimeDirectoryMode=0750' deploy/systemd/system/anas-container-agent.service
	grep -Fqx 'KillMode=mixed' deploy/systemd/system/anas-container-agent.service
	grep -Fqx 'User=a-nas-shares' deploy/systemd/system/anas-shares.service
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-api share-service' deploy/systemd/system/anas-shares.service
	grep -Fq 'anas-shares-system.service.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fqx 'User=a-nas-photos' deploy/systemd/system/anas-photos.service
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-api photo-service' deploy/systemd/system/anas-photos.service
	grep -Fqx 'ReadWritePaths=/srv/a-nas/data' deploy/systemd/system/anas-photos.service
	grep -Fqx 'RuntimeDirectory=a-nas-photos' deploy/systemd/system/anas-photos.service
	grep -Fqx 'PrivateNetwork=true' deploy/systemd/system/anas-photos.service
	grep -Fqx 'SupplementaryGroups=a-nas-photos a-nas-shares' deploy/systemd/system/anas-api.service
	! grep -Eq '^(User|Group)=' deploy/systemd/system/anas-host-agent.service
	grep -Fqx 'RuntimeDirectory=a-nas a-nas-sessions a-nas-share-sessions' deploy/systemd/system/anas-host-agent.service
	grep -Fq 'ANAS_PHOTO_SESSION_GROUP=a-nas-photos' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'anas-photos-system.service.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fqx 'ListenStream=/run/a-nas-ai/ai.sock' deploy/systemd/system/anas-ai.socket
	grep -Fq '"/run/a-nas-ai/ai.sock"' cmd/anas-api/config.go
	grep -Fqx 'SocketGroup=a-nas-photos' deploy/systemd/system/anas-ai.socket
	grep -Fqx 'SocketMode=0660' deploy/systemd/system/anas-ai.socket
	grep -Fqx 'DynamicUser=true' deploy/systemd/system/anas-ai.service
	grep -Fqx 'PrivateNetwork=true' deploy/systemd/system/anas-ai.service
	grep -Fqx 'RestrictAddressFamilies=AF_UNIX' deploy/systemd/system/anas-ai.service
	grep -Fqx 'InaccessiblePaths=-/srv/a-nas' deploy/systemd/system/anas-ai.service
	grep -Fq 'anas-ai-system.service.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq 'ai-worker.tar.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq 'ai-detector.json.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq 'ai-face-detector.json.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq 'ai-face-embedder.json.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq 'ai-animal-embedder.json.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq -- '--detector /opt/a-nas/current/ai/detector.onnx --detector-manifest /opt/a-nas/current/ai/detector.json' deploy/systemd/system/anas-ai.service
	grep -Fq -- '--face-detector /opt/a-nas/current/ai/face-detector.onnx --face-detector-manifest /opt/a-nas/current/ai/face-detector.json --face-embedder /opt/a-nas/current/ai/face-embedder.onnx --face-embedder-manifest /opt/a-nas/current/ai/face-embedder.json' deploy/systemd/system/anas-ai.service
	grep -Fq -- '--animal-embedder /opt/a-nas/current/ai/animal-embedder.onnx --animal-embedder-manifest /opt/a-nas/current/ai/animal-embedder.json' deploy/systemd/system/anas-ai.service
	grep -Fq 'ai_roles=(model:litertlm detector:onnx face-detector:onnx face-embedder:onnx animal-embedder:onnx)' scripts/install-v1.0.1-system-services.sh
	! grep -q '^CPUQuota=' deploy/systemd/system/anas-ai.service
	grep -Fq 'anas-ai.service.d/cpu-reserve.conf' scripts/install-v1.0.1-system-services.sh
	grep -Fqx 'MemoryMax=3G' deploy/systemd/system/anas-ai.service
	grep -Fqx 'Slice=anas-ai.slice' deploy/systemd/system/anas-ai.service
	grep -Fqx 'CPUWeight=10' deploy/systemd/system/anas.slice
	grep -Fq 'anas-system.slice.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq '/etc/systemd/system/anas.slice' scripts/install-v1.0.1-system-services.sh
	grep -Fqx 'ListenStream=/run/a-nas-media/media.sock' deploy/systemd/system/anas-media.socket
	grep -Fq '"/run/a-nas-media/media.sock"' cmd/anas-api/config.go
	grep -Fqx 'Accept=yes' deploy/systemd/system/anas-media.socket
	grep -Fqx 'SocketGroup=a-nas-photos' deploy/systemd/system/anas-media.socket
	grep -Fqx 'SocketMode=0660' deploy/systemd/system/anas-media.socket
	grep -Fqx 'ExecStart=/opt/a-nas/current/anas-api media-worker' deploy/systemd/system/anas-media@.service
	grep -Fqx 'StandardInput=socket' deploy/systemd/system/anas-media@.service
	grep -Fqx 'StandardOutput=journal' deploy/systemd/system/anas-media@.service
	grep -Fqx 'DynamicUser=true' deploy/systemd/system/anas-media@.service
	grep -Fqx 'PrivateNetwork=true' deploy/systemd/system/anas-media@.service
	grep -Fqx 'RestrictAddressFamilies=AF_UNIX' deploy/systemd/system/anas-media@.service
	grep -Fq 'InaccessiblePaths=-/srv/a-nas -/var/lib/a-nas' deploy/systemd/system/anas-media@.service
	grep -Fq 'anas-media-system@.service.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq 'anas-media-system.slice.incoming' scripts/deploy-dev.ps1 scripts/remote-activate-release.sh
	grep -Fq '/etc/systemd/system/system-anasmedia.slice' scripts/install-v1.0.1-system-services.sh
	grep -Fqx 'Slice=system-anasmedia.slice' deploy/systemd/system/anas-media@.service
	grep -Fqx 'MemoryMax=3G' deploy/systemd/system/system-anasmedia.slice
	grep -Fqx 'MaxConnections=16' deploy/systemd/system/anas-media.socket
	grep -Fq '"/run/a-nas-media/slots"' cmd/anas-api/config.go
	grep -Fqx 'const Slots = 8' internal/mediaworker/admission.go
	grep -Fq 'for slot in 0 1 2 3 4 5 6 7; do' scripts/install-v1.0.1-system-services.sh
	grep -Fq 'ffmpeg ffprobe heif-thumbnailer; do' scripts/install-v1.0.1-system-services.sh
	! grep -E '^[A-Za-z0-9_.-]+==' ai/requirements.lock | grep -v -- ' --hash=sha256:[0-9a-f]\{64\}$$'

vet:
	$(GO) vet ./...

go-lint:
	$(GOLANGCI_LINT) run ./...

# Fails on known vulnerabilities that the code can reach. The scanner is pinned
# by the tool directive in go.mod; it fetches the database from vuln.go.dev.
vuln-check:
	$(GO) tool govulncheck ./...

test:
	$(GO) test ./...

# CI runs this beside `make check`; the Web UI tests need the built assets.
race-test: web-build
	$(GO) test -race -p $(RACE_PARALLEL) ./...

# Ruff as ai/requirements-dev.lock pins it: another version may judge differently.
ai-lint:
	@want="$$(sed -n 's/^ruff==\([0-9.]*\).*/\1/p' ai/requirements-dev.lock)"; \
	have="$$($(RUFF) --version 2>/dev/null | cut -d ' ' -f 2)"; \
	if [ "$$have" != "$$want" ]; then echo "ai/requirements-dev.lock pins ruff $$want; found: $${have:-none}" >&2; exit 1; fi
	cd ai && $(RUFF) check .

# The AI Worker's tests need only the Python standard library; tests with the
# real model run when ANAS_AI_MODEL names it (docs/development/LOCAL_ENVIRONMENT.md).
ai-test:
	cd ai && PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -t .

# Modifies accounts, groups, and Samba state: disposable privileged container only.
root-integration-test:
	ANAS_ROOT_INTEGRATION=1 $(GO) test -tags rootintegration -count=1 ./internal/hostops/linux

# Installs the release into a disposable Debian 13 systemd container; needs
# Docker and the image from scripts/build-system-test-image.sh.
system-test: build-binaries
	scripts/system-test.sh $(SYSTEM_TEST_IMAGE)

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

check-steps: web-typecheck web-lint web-test web-build fmt-check docs-check ops-check vet go-lint vuln-check test ai-lint ai-test build-binaries

clean:
	rm -f $(BUILD_DIR)/anas-api $(BUILD_DIR)/anas-host-agent $(BUILD_DIR)/anas-container-agent $(VALIDATION_MANIFEST)
	find internal/webui/dist -mindepth 1 ! -name '.keep' -delete
