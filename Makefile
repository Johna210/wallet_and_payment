# Variables from .env (if present) are exported to every recipe.
-include .env
export

SQLC_VERSION         := 1.31.1
GOLANGCI_LINT_VERSION := v2.14.0

.PHONY: run migrate build test test-integration sqlc lint up down

run: ## Run the service locally against DATABASE_URL
	go run ./cmd/wallet serve

migrate: ## Apply pending migrations to DATABASE_URL
	go run ./cmd/wallet migrate

build: ## Build the binary into bin/
	CGO_ENABLED=0 go build -o bin/wallet ./cmd/wallet

test: ## Unit tests only (no Docker needed)
	go test -race ./...

test-integration: ## Unit and integration tests (needs Docker)
	go test -race -tags integration -count=1 ./...

sqlc: ## Regenerate type-safe query code
	@if ls internal/store/postgres/queries/*.sql >/dev/null 2>&1; then \
		docker run --rm -u "$$(id -u):$$(id -g)" -v "$(CURDIR):/src" -w /src sqlc/sqlc:$(SQLC_VERSION) generate; \
	else \
		echo "sqlc: no queries yet in internal/store/postgres/queries, nothing to generate"; \
	fi

lint: ## Static analysis
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --build-tags integration ./...

up: ## Start the full stack
	docker compose up --build

down: ## Stop the stack and delete its data
	docker compose down -v
