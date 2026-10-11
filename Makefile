# Self-documenting Makefile: `make` or `make help` lists every target with a `## description`.
.DEFAULT_GOAL := help

# Developer tools are pinned here and installed into ./bin by `make setup`,
# so every contributor and CI run uses the same versions.
BUF_VERSION                  := v1.73.0
PROTOC_GEN_GO_VERSION        := v1.36.12
PROTOC_GEN_GO_GRPC_VERSION   := v1.6.2
PROTOC_GEN_CONNECT_GO_VERSION := v1.21.0
PROTOC_GEN_OPENAPI_VERSION   := v0.28.0
SQLC_VERSION                 := v1.31.1
GOLANGCI_LINT_VERSION        := v2.14.0
GOVULNCHECK_VERSION          := v1.9.0

BIN      := $(CURDIR)/bin
export PATH := $(BIN):$(PATH)
GO       ?= go
# Build tools with the same Go version as the module, so linters understand its code.
TOOLCHAIN := $(shell $(GO) env GOVERSION)
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X github.com/panelarr/meta/internal/buildinfo.Version=$(VERSION)
IMAGE    ?= panelarr/meta:dev

.PHONY: help setup build test test-short lint fmt fmt-check vet vuln buf-lint gen gen-check run docker clean check

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_.-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

setup: ## Install pinned developer tools into ./bin and download modules
	@mkdir -p $(BIN)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install connectrpc.com/connect/cmd/protoc-gen-connect-go@$(PROTOC_GEN_CONNECT_GO_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install github.com/sudorandom/protoc-gen-connect-openapi@$(PROTOC_GEN_OPENAPI_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	GOTOOLCHAIN=$(TOOLCHAIN) GOBIN=$(BIN) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) mod download

build: ## Build the meta binary into ./bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/meta ./cmd/meta

test: ## Run all tests with the race detector (Postgres via testcontainers or META_TEST_DATABASE_URL)
	$(GO) test -race -cover ./...

test-short: ## Run tests that need no Postgres or Docker
	$(GO) test -short -race ./...

fmt: ## Format Go and proto files
	$(GO) fmt ./...
	@if [ -x $(BIN)/buf ] && [ -d proto ]; then $(BIN)/buf format -w; fi

fmt-check: ## Fail if Go files are not formatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt-ed:"; echo "$$out"; exit 1; fi

vet: ## go vet
	$(GO) vet ./...

lint: ## golangci-lint and buf lint
	$(BIN)/golangci-lint run ./...
	@if [ -d proto ]; then $(BIN)/buf lint; fi

buf-lint: ## Lint protos only
	$(BIN)/buf lint

vuln: ## Check dependencies and the toolchain for known vulnerabilities
	$(BIN)/govulncheck ./...

gen: ## Generate code from protos (Go, Connect, gRPC, OpenAPI) and SQL (sqlc)
	@if [ -d proto ]; then $(BIN)/buf generate; fi
	@if [ -f sqlc.yaml ]; then $(BIN)/sqlc generate; fi

gen-check: gen ## Fail if generated code is out of date
	@git diff --exit-code -- gen store/db openapi || (echo "generated code is stale: run make gen" && exit 1)

run: build ## Run meta locally (needs META_DATABASE_URL) and print its URL
	@echo "meta listening on http://localhost:8080"
	$(BIN)/meta serve

docker: ## Build the container image ($(IMAGE))
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

clean: ## Remove build artifacts (keeps installed tools)
	rm -f $(BIN)/meta
	rm -rf dist coverage.out

check: fmt-check vet lint vuln test ## Format check, lint, static analysis, vulnerabilities and tests (run before every report/PR)
