# Civic Scanner

Tracks public meetings of Southern California transportation agencies and delivers agendas, transcripts, and AI summaries to subscribers.
A Go API and a Next.js front end in one repo, both generated from a shared OpenAPI contract.

## Layout

| Path | What |
| --- | --- |
| `openapi/` | The API contract, one file per version (`v1.yaml`). Source of truth. |
| `api/` | Go API. Migrations in `api/migrations/`, generated server types in `api/internal/httpapi/v1/`. |
| `web/` | Next.js front end (pnpm). Generated client types in `web/src/lib/api/v1.d.ts`. |
| `.github/` | CI workflow, Dependabot, the ruleset for `main`. |

## Prerequisites

- Go (version in `api/go.mod`) and [golangci-lint v2](https://golangci-lint.run)
- Node (version in `web/.nvmrc`) and pnpm (version in `web/package.json`; `corepack enable` picks it up)
- Docker, for local Postgres

## Quickstart

```sh
make install          # web dependencies
make dev-db           # Postgres 17 on localhost:5432 (db, user, password: civic)
make migrate          # apply migrations
make test             # unit tests
make ci               # everything CI runs
```

`make help` lists every target. The Makefile defaults `DATABASE_URL` to the compose database.

## OpenAPI-first workflow

1. Edit `openapi/v1.yaml`.
2. Run `make generate` (oapi-codegen for Go, openapi-typescript for TypeScript).
3. Commit the spec and the generated code together.

Generated files are committed and never edited by hand. CI regenerates them and fails on any diff.

## CI

`.github/workflows/ci.yml` runs on every pull request and on pushes to `main`. Jobs run only when their paths change; a spec change runs both `api` and `web`.

| Job | Checks |
| --- | --- |
| `openapi-lint` | Redocly lint of `openapi/*.yaml` |
| `api` | build, vet, golangci-lint, unit tests with `-race`, codegen drift |
| `api-integration` | migrations, then integration tests, against a Postgres 17 container |
| `migrations-lint` | Squawk on changed migrations |
| `web` | lint, format, typecheck, tests, codegen drift, build |
| `ci-ok` | passes when none of the above failed; the one required check on `main` |

Branch rules for `main` are in `.github/rulesets/` (import instructions there).

## Migrations

Migrations are goose SQL files in `api/migrations/` and follow expand and contract:

- **Expand:** only add tables or columns that old code ignores. Squawk fails CI on destructive or locking changes.
- **Release:** new code that uses them ships behind a flag.
- **Contract:** a later migration removes the old structures. Mark the file with a `-- migration: contract` line, which permits dropping columns and tables, and confirm the flag is fully on in production before merging.

## Deploy

Merging to `main` deploys both sides.

- **API on Railway**, configured by `api/railway.json`. Migrations run as the pre-deploy step, so a failed migration stops the deploy. Watch paths limit redeploys to `api/` and `openapi/`.
- **Web on Vercel**, with the project root directory set to `web`. Each pull request gets a preview deploy.
