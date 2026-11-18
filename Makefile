REDOCLY_CLI_VERSION ?= 2.78.0
GO_COVERAGE_MIN ?= 90
GOLANGCI_LINT_VERSION ?= v2.15.0

GO_VERSION := $(shell awk '$$1 == "go" {print $$2}' api/go.mod)
GOLANGCI_LINT_DIR := $(CURDIR)/api/bin/golangci-lint-$(GOLANGCI_LINT_VERSION)-go$(GO_VERSION)
GOLANGCI_LINT := $(GOLANGCI_LINT_DIR)/golangci-lint

DATABASE_URL ?= postgres://civic:civic@localhost:5432/civic?sslmode=disable
export DATABASE_URL

STORAGE_ENDPOINT ?= http://localhost:9090
STORAGE_BUCKET ?= civic-audio
STORAGE_ACCESS_KEY_ID ?= civic
STORAGE_SECRET_ACCESS_KEY ?= civic-secret
STORAGE_PATH_STYLE ?= true
export STORAGE_ENDPOINT STORAGE_BUCKET STORAGE_ACCESS_KEY_ID STORAGE_SECRET_ACCESS_KEY STORAGE_PATH_STYLE

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: install
install: ## Install web dependencies
	cd web && pnpm install --frozen-lockfile

.PHONY: dev-db
dev-db: ## Start local Postgres and S3-compatible object store (docker compose) and wait until healthy
	docker compose up -d --wait postgres objectstore

.PHONY: dev-db-down
dev-db-down: ## Stop local Postgres and object store (the Postgres data volume is kept)
	docker compose down

.PHONY: migrate
migrate: ## Apply goose + River migrations to DATABASE_URL
	cd api && go run ./cmd/migrate up

.PHONY: generate
generate: ## Regenerate Go and TypeScript code from openapi/
	cd api && go generate ./...
	cd web && pnpm generate

.PHONY: generate-check
generate-check: generate ## Fail if generated code differs from what is committed
	@if [ -n "$$(git status --porcelain -- api web)" ]; then \
		git status --short -- api web; \
		echo "Generated code is out of date (or api/ and web/ have uncommitted changes)."; \
		exit 1; \
	fi

.PHONY: openapi-lint
openapi-lint: ## Lint the OpenAPI specs with Redocly
	REDOCLY_TELEMETRY=off npx --yes @redocly/cli@$(REDOCLY_CLI_VERSION) lint openapi/*.yaml

.PHONY: lint-migrations
lint-migrations: ## Lint SQL migrations with Squawk
	.github/scripts/lint-migrations.sh

$(GOLANGCI_LINT):
	GOBIN=$(GOLANGCI_LINT_DIR) GOTOOLCHAIN=go$(GO_VERSION) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: lint-api
lint-api: $(GOLANGCI_LINT) ## go vet + golangci-lint (the CI version, built on first use)
	cd api && go vet ./... && $(GOLANGCI_LINT) run

.PHONY: lint-web
lint-web: ## eslint, prettier check, typecheck
	cd web && pnpm lint && pnpm format:check && pnpm typecheck

.PHONY: lint
lint: openapi-lint lint-migrations lint-api lint-web ## Run every linter

.PHONY: build
build: ## Build api and web
	cd api && go build ./...
	cd web && pnpm build

.PHONY: test
test: ## Unit tests (api with -race, web)
	cd api && go test -race ./...
	cd web && pnpm test

.PHONY: test-integration
test-integration: migrate ## Migrate, then run api integration tests (needs Postgres and the object store: make dev-db)
	cd api && go test -tags=integration ./...

.PHONY: coverage
coverage: ## Unit + integration coverage for api (needs make dev-db) and web; fails below the minimums
	GO_COVERAGE_MIN=$(GO_COVERAGE_MIN) .github/scripts/go-coverage.sh "Go unit + integration" coverage.out integration
	cd web && pnpm test:coverage

.PHONY: prepush
prepush: install lint test generate-check build ## Run before every push: install, every linter, unit tests, codegen drift, build (no Docker)

.PHONY: ci
ci: install lint test generate-check build dev-db coverage ## Everything CI runs
