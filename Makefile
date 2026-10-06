GO ?= go
BUILD_DIR ?= build
VERSION ?= dev

.PHONY: all fmt fmt-check docs-check vet test build check clean

all: check

fmt:
	gofmt -w $$(find . -name '*.go' -type f -not -path './.git/*')

fmt-check:
	@files="$$(gofmt -l $$(find . -name '*.go' -type f -not -path './.git/*'))"; \
	if [ -n "$$files" ]; then echo "The following files need gofmt:"; echo "$$files"; exit 1; fi

docs-check:
	$(GO) run ./tools/doccheck

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

build:
	mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BUILD_DIR)/anas-api ./cmd/anas-api
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BUILD_DIR)/anas-host-agent ./cmd/anas-host-agent

check: fmt-check docs-check vet test build

clean:
	rm -f $(BUILD_DIR)/anas-api $(BUILD_DIR)/anas-host-agent
