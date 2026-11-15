# Thin wrappers over the commands CI runs. `make help` lists targets.

# Tool versions; keep in sync with .github/workflows/ci.yml.
REDOCLY_CLI_VERSION ?= 2.78.0

# Matches docker-compose.yml. Override to point at another database.
DATABASE_URL ?= postgres://civic:civic@localhost:5432/civic?sslmode=disable
export DATABASE_URL

.DEFAULT_GOAL := help

.PHONY: help
help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: install
install: ## Install web dependencies
	cd web && pnpm install --frozen-lockfile

.PHONY: dev-db
dev-db: ## Start local Postgres (docker compose) and wait until healthy
	docker compose up -d --wait postgres

.PHONY: dev-db-down
dev-db-down: ## Stop local Postgres (the data volume is kept)
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

.PHONY: lint-api
lint-api: ## go vet + golangci-lint (v2, must be installed)
	cd api && go vet ./... && golangci-lint run

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
test-integration: migrate ## Migrate, then run api integration tests (needs Postgres: make dev-db)
	cd api && go test -tags=integration ./...

.PHONY: ci
ci: install lint test generate-check build dev-db test-integration ## Everything CI runs
