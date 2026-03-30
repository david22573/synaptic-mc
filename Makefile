.PHONY: help install run-server run-bot dev clean test-replay build run-prod

# Default target
.DEFAULT_GOAL := help

help: ## Show this help message
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

install: ## Install Go and JS dependencies
	@echo "==> Installing JS dependencies..."
	cd js && npm install
	@echo "==> Tidying Go modules..."
	go mod tidy

build: ## Compile Go binary and TypeScript bot for optimal performance
	@echo "==> Compiling TypeScript bot..."
	cd js && npx tsc
	@echo "==> Building Go control plane..."
	go build -ldflags="-s -w" -o bin/synaptic-server ./cmd/server

run-prod: ## Run the optimized compiled binaries
	@echo "==> Starting production server..."
	./bin/synaptic-server -bot-script="js/dist/index.js"

run-server: ## Run the Go control plane directly
	@echo "==> Starting Go WebSocket Server..."
	go run cmd/server/*.go

run-bot: ## Run the Mineflayer TypeScript bot directly
	@echo "==> Starting Mineflayer Bot..."
	cd js && npx tsx index.ts

dev: ## Run both server and bot concurrently in one terminal
	@echo "==> Starting CraftD Control Plane and Bot..."
	@trap 'echo "Shutting down..."; kill 0' SIGINT; \
	go run cmd/server/*.go & \
	sleep 2; \
	cd js && npx tsx index.ts

test-replay: ## Run the Replay Test Harness
	@echo "==> Running Planning Replay Tests..."
	go test -v ./tests/...

clean: ## Clean up Go cache and Node modules
	@echo "==> Cleaning up environment..."
	rm -rf js/node_modules
	rm -rf js/dist
	rm -rf bin
	go clean