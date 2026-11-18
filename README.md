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

`make help` lists every target. The Makefile defaults `DATABASE_URL` to the compose database; override it to point at another database.

The binaries read configuration from the environment only and do not load `.env` files. `api/.env.example` lists the variables (`LOG_LEVEL`: `debug`, `info`, `warn`, `error`; `APP_ENV`: `development`, `production`); copy it to `.env` and export it. For the web app, copy `web/.env.example` to `web/.env.local`; `NEXT_PUBLIC_API_BASE_URL` includes the `/v1` prefix.

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
| `api` | build, vet, golangci-lint, unit tests with `-race` and coverage, codegen drift |
| `api-integration` | unit and integration tests with `-race` and coverage, against a Postgres 17 container; fails below the coverage minimum |
| `migrations-lint` | Squawk on changed migrations |
| `web` | lint, format, typecheck, tests with coverage, codegen drift, build |
| `coverage-comment` | posts or updates the coverage comment on the pull request; not part of `ci-ok` |
| `ci-ok` | passes when none of the above failed; the one required check on `main` |

Branch rules for `main` are in `.github/rulesets/` (import instructions there).

Notes on the workflow:

- Path filtering lives in the `changes` job, not in `on.paths`, so the workflow always starts and `ci-ok` always reports. Skipped jobs count as passing; failed or cancelled jobs fail it.
- A new push to a pull request cancels the run for the previous push. Runs on `main` are keyed by commit and never cancelled.
- `migrations-lint` lints the migrations added or modified by the change. If none were (only the linter config or the workflow changed), it lints all of them.
- Tool versions (Redocly CLI, Squawk, golangci-lint) and `GO_COVERAGE_MIN` are pinned in the workflow `env` and mirrored in the `Makefile` and `.github/scripts/lint-migrations.sh`. Bump them together.
- **Planned: compatibility test.** Once there is a production version, add a job that applies the pull request's migrations, checks out the deployed commit (for example a `production` tag moved on deploy) into a second directory, and runs its `go test -tags=integration ./...` against that database. This proves each migration is backward compatible with the code in production. Add the job to the `needs` list of `ci-ok`.

## Coverage

`make coverage` (after `make dev-db`) measures both sides the way CI does.

- **Go:** `.github/scripts/go-coverage.sh` runs `go test -race` with `-coverpkg` over every package, so a test counts toward all the code it reaches, not only its own package. It excludes generated code (`internal/httpapi/v1`) and the test helpers `internal/testbin` and `internal/testdb`. The `api` job reports unit coverage only; `api-integration` runs with `-tags=integration`, which includes the unit tests, and reports the combined number. That combined number must stay at or above `GO_COVERAGE_MIN` (90%).
- **Web:** `pnpm test:coverage` runs Vitest with V8 coverage over `src/`, excluding tests and `.d.ts` files. `web/vitest.config.ts` sets a 90% minimum for statements, branches, functions, and lines.

Each job writes its totals to the run summary and uploads the reports as artifacts: `go-coverage-unit`, `go-coverage` (profile, `coverage.html`, and the total in `coverage.total`), and `web-coverage` (HTML, lcov, and `coverage-summary.json`). Locally, open `api/coverage.html` and `web/coverage/index.html`.

On pull requests, the `coverage-comment` job keeps one comment up to date, starting with `### Coverage`. It shows each total for the pull request, the same total on the pull request's base branch, and the change. It edits that comment on every push instead of adding new ones.

- `.github/scripts/coverage-baseline.sh` downloads the base branch's reports. It picks the newest unexpired artifact of each name from a run on the base branch, so a push that skipped the Go jobs does not erase the Go baseline. Artifacts expire after 90 days; with no recent run on the base branch, the comment says there is no baseline.
- `.github/scripts/coverage-comment.sh` renders the comment. A row is "not run" when path filtering skipped its job.
- The job runs only for pull requests from this repository, since a fork's token cannot write comments. It needs `pull-requests: write` and `actions: read`, granted to that job only.

Test helpers:

- `internal/testdb`: `testdb.Pool(t)` opens `DATABASE_URL` and applies migrations; `testdb.WaitForJobState` polls a River job until it reaches a state.
- `internal/testbin`: builds the package under test into a real binary and runs it. Each binary is tested this way for its exit code on bad input and, in the integration tests, for a clean exit on SIGTERM. When the coverage script sets `TESTBIN_COVERPKG` and `TESTBIN_COVERDIR`, the binary is built with `-cover` and writes to that directory; the script converts it with `go tool covdata` and merges it into the profile. Without them, the binary is built plainly.

The binaries' `run` functions take a context, so tests also call them in process. Uncovered by design: error branches with no way to trigger them, such as goose or River failing after the connection is up, or the HTTP server failing to shut down.

## Migrations

Migrations are goose SQL files in `api/migrations/` and follow expand and contract:

- **Expand:** only add tables or columns that old code ignores. Squawk fails CI on destructive or locking changes.
- **Release:** new code that uses them ships behind a flag.
- **Contract:** a later migration removes the old structures. Mark the file with a `-- migration: contract` line, which permits dropping columns and tables, and confirm the flag is fully on in production before merging.

`make lint-migrations` runs `.github/scripts/lint-migrations.sh [file...]` (default: every file in `api/migrations/`), which adapts each goose file for Squawk:

- Only the `-- +goose Up` half is linted. A rollback is destructive by design and would always fail.
- Squawk assumes a transaction unless the file has a `-- +goose NO TRANSACTION` line, matching what goose does.
- In a contract file, `ban-drop-column` and `ban-drop-table` are excluded. Everything else is still checked.

`.squawk.toml` excludes `require-lock-timeout` and `require-statement-timeout`: timeouts are a connection-level concern for the migrate command, not something to repeat in every file. `--exclude` on the command line replaces the config list, so `CONTRACT_EXCLUDE` in the script repeats those two rules. Keep the two lists in sync.

## Deploy

Merging to `main` deploys both sides.

- **API on Railway**, configured by `api/railway.json`, with the service root directory set to `/api` (the Docker build context). The `RAILWAY_GIT_COMMIT_SHA` build arg becomes the version the health endpoint reports. Migrations run as the pre-deploy step, so a failed migration stops the deploy. Watch paths limit redeploys to `api/` and `openapi/`.
- **Web on Vercel**, with the project root directory set to `web`. Each pull request gets a preview deploy.
