.PHONY: help install build build-ui build-ts build-go run-prod run-server run-bot dev clean test-replay

# Default target
.DEFAULT_GOAL := help

ifeq ($(OS),Windows_NT)
	BIN_NAME = bin/synaptic-server.exe
else
	BIN_NAME = bin/synaptic-server
endif

help: ## Show help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install: ## Install dependencies
	@echo "==> Installing JS dependencies..."
	cd js && pnpm install
	@echo "==> Tidying Go modules..."
	go mod tidy

build-ui: ## Build Svelte UI (production)
	@echo "==> Building UI..."
	cd ui && npm run build

build-ts: ## Compile TypeScript bot to dist/
	@echo "==> Building TypeScript bot..."
	cd js && pnpm install && npx tsc --project tsconfig.json

build-go: ## Build Go control plane
	@echo "==> Building Go binary..."
	go build -ldflags="-s -w -X main.buildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)" -o $(BIN_NAME) ./cmd/server

build: build-ui build-ts build-go ## Full production build (recommended)

run-prod: build ## Run production binaries
	@echo "==> Starting production server..."
	./$(BIN_NAME) -http=:8080 -data-dir=data -bot-script=js/dist/index.js -hesitation-ms=180 -noise-level=0.03

run-server: ## Run Go server (dev)
	go run cmd/server/*.go

run-bot: ## Run bot directly (dev)
	cd js && npx tsx index.ts

dev: ## Development mode (Go spawns bot)
	go run cmd/server/*.go

clean: ## Clean everything
	rm -rf js/dist ui/dist bin data
	go clean -cache

test-replay: ## Run replay tests
	go test -v ./tests/...