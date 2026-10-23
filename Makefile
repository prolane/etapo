GO ?= go
BINARY := etapo
DIST := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
CONFIG ?= config.example.yaml

.PHONY: help build run test vet fmt fmt-check lint ci cross clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

build: ## Build the binary for the local machine into bin/
	@mkdir -p bin
	$(GO) build -ldflags="$(LDFLAGS)" -o bin/$(BINARY) ./cmd/etapo

run: ## Run locally with the example config (mock devices)
	$(GO) run ./cmd/etapo run --config $(CONFIG)

test: ## Run all tests with the race detector
	$(GO) test -race ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format all Go files in place
	gofmt -w .

fmt-check: ## Fail if any Go file is not gofmt-clean
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "Files need gofmt:"; echo "$$unformatted"; exit 1; fi

lint: fmt-check vet ## Run format check and vet

ci: lint test cross ## Run everything CI runs

cross: ## Cross-compile all release targets into dist/
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64 ./cmd/etapo
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 $(GO) build -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-armv7 ./cmd/etapo
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 ./cmd/etapo
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64 ./cmd/etapo
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64 ./cmd/etapo
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -ldflags="$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe ./cmd/etapo

clean: ## Remove build outputs
	rm -rf bin $(DIST)
