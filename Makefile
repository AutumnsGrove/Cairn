SHELL := /bin/bash

# Cairn: the real binary, the offline dev stack and every check CI runs.
#
# Pure Go, no cgo in any build. `go test -race` is the only place cgo is allowed to appear, because the race
# runtime needs it; the `nocgo` target proves nothing of ours does.

GO         ?= go
PNPM       ?= pnpm
BIN        ?= bin/cairn
DEV_BIN    ?= bin/cairn-dev
WEB_DIR    := web
EMBED_DIR  := internal/webui/dist
FUZZTIME   ?= 10s

GO_FILES := $(shell find . -name '*.go' -not -path './$(WEB_DIR)/node_modules/*')
GOVERSION := $(shell $(GO) env GOVERSION)

.DEFAULT_GOAL := check
.PHONY: help check fmt fmt-check vet lint test test-short race fuzz-smoke nocgo web web-check e2e gen \
        guard gitleaks build dev clean tidy version verify-embed

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## --- checks --------------------------------------------------------------------------------------------

check: fmt-check vet lint test fuzz-smoke web-check guard ## Everything CI requires, locally
	@echo "check: all green"

fmt: ## Format Go, the frontend and the docs
	$(GO) run mvdan.cc/gofumpt@v0.12.0 -l -w . || gofumpt -w .
	@if [ -d $(WEB_DIR)/node_modules ]; then cd $(WEB_DIR) && $(PNPM) run format; fi

fmt-check: ## Fail if any Go file is not gofumpt-clean
	@out="$$(gofumpt -l $(GO_FILES) 2>/dev/null || $(GO) run mvdan.cc/gofumpt@v0.12.0 -l $(GO_FILES))"; \
	if [ -n "$$out" ]; then echo "not gofumpt-clean:"; echo "$$out"; exit 1; fi
	@echo "fmt-check: clean"

vet: ## go vet
	$(GO) vet ./...

lint: ## staticcheck and golangci-lint
	staticcheck ./...
	golangci-lint run

test: ## go test -race ./... (the only cgo use)
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

test-short: ## Unit tests without the race runtime, for a fast loop
	$(GO) test -count=1 ./...

fuzz-smoke: ## A short run of every fuzz target; guards the parsers that take untrusted input
	@set -e; \
	grep -rhoE '^func (Fuzz[A-Za-z0-9_]+)' --include='*_test.go' . | awk '{print $$2}' | sort -u | while read -r t; do \
	  grep -rlE "^func $${t}\(" --include='*_test.go' . | sort | while read -r file; do \
	    pkg="$$(dirname "$${file}")"; \
	    echo "fuzz-smoke: $${t} in $${pkg}"; \
	    ( cd "$${pkg}" && $(GO) test -run '^$$' -fuzz "^$${t}$$" -fuzztime=$(FUZZTIME) . ); \
	  done; \
	done

nocgo: ## Prove the whole module builds with cgo off, and compiles for the potato
	CGO_ENABLED=0 $(GO) build ./...
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -o /dev/null ./cmd/cairn

## --- frontend ------------------------------------------------------------------------------------------

web: ## Build SvelteKit into a scratch directory and copy it into the embed directory
	@test -f $(WEB_DIR)/package.json || { echo "web: no $(WEB_DIR)/package.json yet"; exit 1; }
	cd $(WEB_DIR) && $(PNPM) install --frozen-lockfile
	cd $(WEB_DIR) && $(PNPM) run build
	@rm -rf $(EMBED_DIR)
	@mkdir -p $(EMBED_DIR)
	@cp -R $(WEB_DIR)/build/. $(EMBED_DIR)/
	@# The placeholder is the only file under the embed directory that is ever committed.
	@touch $(EMBED_DIR)/.gitkeep
	@echo "web: built into $(EMBED_DIR)"

web-check: ## svelte-check, ESLint, Prettier and Vitest with zero warnings
	@test -f $(WEB_DIR)/package.json || { echo "web-check: no $(WEB_DIR)/package.json yet"; exit 1; }
	cd $(WEB_DIR) && $(PNPM) install --frozen-lockfile
	cd $(WEB_DIR) && $(PNPM) run check
	cd $(WEB_DIR) && $(PNPM) run lint
	cd $(WEB_DIR) && $(PNPM) run format:check
	cd $(WEB_DIR) && $(PNPM) run test:unit

e2e: ## The Playwright suite against the real binary and the world (installs browsers on first run)
	@test -d $(WEB_DIR)/node_modules || { echo "e2e: run make web-check once first"; exit 1; }
	cd $(WEB_DIR) && $(PNPM) exec playwright install chromium webkit
	cd $(WEB_DIR) && $(PNPM) exec playwright test

## --- contract, guard, build ----------------------------------------------------------------------------

gen: ## Regenerate everything derived from api/ and the SQL
	$(GO) run ./tools/gen
	$(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate
	@test -d $(WEB_DIR)/node_modules || { echo "gen: run make web-check once first (pnpm install)"; exit 1; }
	cd $(WEB_DIR) && $(PNPM) run gen:contract

verify-embed: ## Only the placeholder may be committed under the embed directory
	@bash scripts/guard.sh embed

guard: ## gitleaks, embed placeholder, no live provider in tests, every fuzz target has seeds
	@bash scripts/guard.sh all

gitleaks: ## Scan the working tree for secrets
	gitleaks detect --no-banner --redact --source .

build: ## Build the binary
	$(GO) build -o $(BIN) ./cmd/cairn

dev: ## The real service against the in-process world, offline
	$(GO) run ./cmd/cairn-dev

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin $(EMBED_DIR)
	@mkdir -p $(EMBED_DIR) && touch $(EMBED_DIR)/.gitkeep

version:
	@echo "$(GOVERSION)"
