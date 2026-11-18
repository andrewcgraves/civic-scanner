# Civic Scanner

## No comments in code

**Do not leave comments in code.** Code explains itself through naming and structure. Do not add explanatory, narrating, TODO, or doc comments, in any language or config file. Information that must be written down goes in `README.md`.

The only exceptions:

- Functional directives, which tools read: `//go:generate`, `//go:build`, `//go:embed`, bare `//nolint:<linter>`, goose `-- +goose ...` annotations, `-- squawk-ignore <rule>`, `-- migration: contract`, `eslint-disable` lines, shebangs, the `# vX.Y.Z` version after a pinned action SHA in workflows, the `## ` target descriptions in the `Makefile` (printed by `make help`), and editor type or schema directives (`# yaml-language-server: $schema=...`, `/** @type {...} */`).
- Generated files, which are never edited by hand: `api/internal/httpapi/v1/api.gen.go`, `web/src/lib/api/v1.d.ts`, `web/pnpm-lock.yaml`.

Do not add a comment to satisfy a linter. The doc-comment lint rules are disabled in `api/.golangci.yml`.

## Layout

- `openapi/`: the API contract (`v1.yaml`). Source of truth for both sides.
- `api/`: Go API. Binaries in `api/cmd/` (`api`, `migrate`, `worker`), goose migrations in `api/migrations/`.
- `web/`: Next.js front end (pnpm, Node 26).
- `.github/`: CI workflow, scripts, Dependabot, ruleset for `main`.
- `.railway/`: Railway infrastructure as code (`railway.ts`). Change Railway settings there and run `railway config plan`; never `railway config apply` without the user's approval.

## OpenAPI-first workflow

1. Edit `openapi/v1.yaml`.
2. Run `make generate`.
3. Commit the spec and the generated output together. CI fails on any drift.

## Commands

Run from the repo root. `make help` lists every target.

- `make install`: web dependencies.
- `make dev-db` / `make dev-db-down`: local Postgres through docker compose.
- `make migrate`: apply goose and River migrations to `DATABASE_URL`.
- `make generate` / `make generate-check`: regenerate code; fail if it differs from what is committed.
- `make prepush`: install, every linter, unit tests, codegen drift and build. No Docker needed.
- `make lint`: `openapi-lint`, `lint-migrations`, `lint-api`, `lint-web`. `lint-api` builds the golangci-lint version CI uses into `api/bin/` on first use; do not use a globally installed one.
- `make test`: unit tests. `make test-integration`: migrates, then runs the API integration tests (needs `make dev-db`).
- `make coverage`: unit and integration coverage for both sides, failing below the minimums (needs `make dev-db`). See "Coverage" in `README.md`.
- `make build`: build both sides.
- In `web/`: `pnpm dev`, `pnpm lint`, `pnpm format`, `pnpm format:check`, `pnpm typecheck`, `pnpm test`, `pnpm test:coverage`, `pnpm generate`, `pnpm build`.

## Before every push

Run `make prepush` before every `git push` and fix everything it reports. Do not push while it fails, and do not rely on CI to find lint errors. Commit first: `generate-check` fails on uncommitted changes under `api/` and `web/`.

Run `make ci` as well when the change touches migrations, the store, jobs or anything else covered by integration tests. It runs everything CI runs and needs Docker.

## Migrations

Migrations follow expand and contract. An ordinary migration only adds structures that old code ignores; Squawk fails CI on destructive or locking changes. A destructive change goes in a separate, later migration containing the line `-- migration: contract`. For such a file `.github/scripts/lint-migrations.sh` excludes the `ban-drop-column` and `ban-drop-table` rules and still checks everything else. Only the `-- +goose Up` half of a file is linted.
