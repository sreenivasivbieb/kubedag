# kubedag — Makefile
#
# `make help` lists targets. Every target is safe to run repeatedly.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

KIND_CLUSTER ?= kubedag
NAMESPACE    ?= kubedag

GO       ?= go
BIN_DIR  := bin

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build all binaries into ./bin
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -o $(BIN_DIR)/orchestrator ./cmd/orchestrator
	$(GO) build -trimpath -o $(BIN_DIR)/podmanager   ./cmd/podmanager
	$(GO) build -trimpath -o $(BIN_DIR)/kubedag-cli ./cmd/cli
	@echo "built: $(BIN_DIR)/{orchestrator,podmanager,kubedag-cli}"

.PHONY: test
test: ## Run unit tests
	$(GO) test ./... -count=1

.PHONY: test-race
test-race: ## Run unit tests with the race detector
	$(GO) test ./... -race -count=1

.PHONY: cover
cover: ## Unit tests with a coverage summary
	$(GO) test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -func=coverage.out | tail -20

.PHONY: vet
vet: ## go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## gofmt in place
	$(GO) fmt ./...

.PHONY: lint
lint: fmt vet ## format + vet

.PHONY: tidy
tidy: ## tidy go.mod
	$(GO) mod tidy

.PHONY: dev-up
dev-up: ## One-command local stack (kind + redis + postgres + app)
	./scripts/dev-up.sh

.PHONY: dev-down
dev-down: ## Tear down the local stack
	./scripts/dev-down.sh

.PHONY: kind-up
kind-up: ## Create the kind cluster only
	./scripts/dev-up.sh --infra-only

.PHONY: image
image: ## Build the runner container image into kind
	./scripts/build-image.sh

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out