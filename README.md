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
| `.railway/` | Railway infrastructure as code (`railway.ts`). |

## Prerequisites

- Go (version in `api/go.mod`) and [golangci-lint v2](https://golangci-lint.run)
- Node (version in `web/.nvmrc`) and pnpm (version in `web/package.json`). Node 25 and later no longer bundle Corepack; install pnpm with `npm install --global corepack && corepack enable`, Homebrew, or the standalone installer. pnpm switches itself to the pinned version.
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

The binaries read configuration from the environment only and do not load `.env` files. `api/.env.example` lists the variables (`LOG_LEVEL`: `debug`, `info`, `warn`, `error`; `APP_ENV`: `development`, `production`; the `AI_GATEWAY_*` and `LLM_*` variables are described under "LLM"); copy it to `.env` and export it. For the web app, copy `web/.env.example` to `web/.env.local`; `NEXT_PUBLIC_API_BASE_URL` includes the `/v1` prefix. The flag variables for both sides are described under [Feature flags](#feature-flags).

## OpenAPI-first workflow

1. Edit `openapi/v1.yaml`.
2. Run `make generate` (oapi-codegen for Go, openapi-typescript for TypeScript).
3. Commit the spec and the generated code together.

Generated files are committed and never edited by hand. CI regenerates them and fails on any diff.

## Feature flags

Unfinished work ships dark behind a flag. Both sides evaluate flags through [OpenFeature](https://openfeature.dev), with PostHog as the provider when a key is set and an in-memory list otherwise. Each side has its own variables.

### Web

The web app evaluates flags through OpenFeature: `@openfeature/server-sdk` on the server and `@openfeature/react-sdk` in the browser. `web/src/lib/flags/` wraps both. Callers import only from there, never from OpenFeature or PostHog, so the provider can change without touching them.

- **Server components:** `await isFlagEnabled("flag-key")` from `@/lib/flags/server`.
- **Client components:** `useFlag("flag-key")` from `@/lib/flags/client`. The root layout wraps the app in `FlagsProvider`, which supplies it.

Every flag is a boolean and defaults to off. An unknown flag, a provider that fails to start, and an evaluation error all evaluate to off.

The provider depends on the environment:

| Variable | Meaning |
| --- | --- |
| `NEXT_PUBLIC_POSTHOG_KEY` | PostHog project API key. When set, PostHog serves flags on both sides and `NEXT_PUBLIC_FEATURE_FLAGS` is ignored. |
| `NEXT_PUBLIC_POSTHOG_HOST` | PostHog API host. Defaults to `https://us.i.posthog.com`. |
| `NEXT_PUBLIC_FEATURE_FLAGS` | Used when there is no PostHog key: a comma-separated list of flags that are on, or `*` for all on. Unset or empty means all off. For local development, tests, and preview deploys. |

Notes:

- `NEXT_PUBLIC_` values are inlined at `next build`, so changing one needs a new build. On Vercel, set them per environment: the PostHog key for Production, and `NEXT_PUBLIC_FEATURE_FLAGS` for Preview.
- PostHog serves flags through its official providers, `@posthog/openfeature-node-provider` (over `posthog-node`) and `@posthog/openfeature-web-provider` (over `posthog-js`). PostHog starts for flags only: no `$feature_flag_called` events, page views, autocapture, session recording, or web vitals. Analytics is a separate change.
- There is no sign-in yet, so the server evaluates every flag for one anonymous distinct ID. Percentage rollouts and person targeting take effect only in the browser; on the server a flag is on or off for everyone.
- In the browser, PostHog flags arrive after the page loads. The server-rendered HTML and the first client render show PostHog flags as off, and they switch on when PostHog answers. In-memory flags are the same on both renders.
- Without a personal API key, `posthog-node` asks PostHog on every server evaluation. If PostHog is unreachable, a render that checks a flag waits for its retry (about 3 seconds) before treating the flag as off.
- To swap providers, change `createProvider` in `web/src/lib/flags/server.ts` and `web/src/lib/flags/client.tsx`.
- The flags module picks its provider once, when it loads. A test sets `NEXT_PUBLIC_FEATURE_FLAGS` with `vi.stubEnv`, calls `vi.resetModules()`, and imports the module again; see `web/src/lib/flags/*.test.*`. Tests never call PostHog: the PostHog cases mock `posthog-node` and `posthog-js`.

### API

The API and the worker evaluate flags through OpenFeature's Go SDK. Handlers and jobs call `featureflags.Enabled(ctx, "new-search")` from `internal/featureflags` and never import a vendor SDK.

Both binaries call `featureflags.Init` at startup, which picks the provider from these variables:

| Variable | Meaning |
| --- | --- |
| `POSTHOG_API_KEY` | PostHog project API key. When set, PostHog decides every flag and `FEATURE_FLAGS` is ignored. |
| `POSTHOG_HOST` | PostHog host. Empty means `https://us.i.posthog.com`; EU projects use `https://eu.i.posthog.com`. With a PostHog key set, anything other than an `http` or `https` URL with a host (for example `us.i.posthog.com` without a scheme) stops the binary at startup. |
| `POSTHOG_FLAG_TIMEOUT` | How long one PostHog flag check may take, as a Go duration such as `500ms`. Default `1s`. A value that does not parse, or is not positive, stops the binary at startup. |
| `FEATURE_FLAGS` | Used only without a PostHog key. A comma-separated list of the flags that are on, such as `new-search,digest-emails`. `*` turns every flag on, for staging-like runs. Empty means every flag is off. |

- A flag is off unless the provider says it is on. These all evaluate to off: an unknown flag, a PostHog error, an unreachable or slow PostHog, a flag PostHog failed to evaluate, a quota-limited project, and a multivariate flag (whatever its variant, since `Enabled` answers only boolean flags).
- What gets logged at `warn`: request errors, timeouts, context deadlines and multivariate flags, all by the provider. posthog-go logs quota limits and responses that report `errorsWhileComputingFlags` itself, through the service logger. posthog-go returns the same `ErrFlagNotFound` for an unknown flag, a flag that failed to evaluate and a quota-limited project, and does not expose which one happened. So the provider logs none of those three itself, and a failed flag is logged only when PostHog also reports `errorsWhileComputingFlags`.
- PostHog evaluates each call remotely, through its `/flags` endpoint. A check makes one attempt, without retries, limited by `POSTHOG_FLAG_TIMEOUT`, and returns off as soon as the caller's context ends, whichever comes first. posthog-go does not take a context, so a request abandoned at the caller's deadline keeps running in the background until `POSTHOG_FLAG_TIMEOUT`. The distinct ID is `civic-scanner-server`, or the OpenFeature targeting key when a caller passes one, so a percentage rollout gives the same answer to every call that has no targeting key. No `$feature_flag_called` events are sent.
- The PostHog provider (`internal/featureflags/posthog.go`) is a thin layer over the official `posthog-go` client, because no maintained OpenFeature Go provider for PostHog exists. Another vendor means another provider in that package; callers do not change.
- OpenFeature's provider is process-wide. A test that calls `featureflags.Init` with `config.FeatureFlags{On: ...}` must not run in parallel with other flag tests. Parallel tests can register OpenFeature's `openfeature/testing` provider instead, which keys flags by test.

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
- **Web:** `pnpm test:coverage` runs Vitest with V8 coverage over `src/`, excluding tests and `.d.ts` files. `web/coverage-thresholds.json` sets a 90% minimum for statements, branches, functions, and lines; `web/vitest.config.ts` enforces it and the coverage comment reads it.

Each job writes its totals to the run summary and uploads the reports as artifacts: `go-coverage-unit`, `go-coverage` (profile, `coverage.html`, and the total in `coverage.total`), and `web-coverage` (HTML, lcov, and `coverage-summary.json`). Locally, open `api/coverage.html` and `web/coverage/index.html`.

On pull requests, the `coverage-comment` job keeps one comment up to date, starting with `### Coverage`. It shows each total for the pull request, the same total on the pull request's base branch, and the change. It edits that comment on every push instead of adding new ones.

- `.github/scripts/coverage-baseline.sh` downloads the base branch's reports. It picks the newest unexpired artifact of each name from a run on the base branch, so a push that skipped the Go jobs does not erase the Go baseline. Artifacts expire after 90 days; with no recent run on the base branch, the comment says there is no baseline.
- `.github/scripts/coverage-comment.sh` renders the comment. Each row shows its minimum and whether it meets it. Go unit-only coverage has no minimum and is marked "reported only". A row is "not run" when path filtering skipped its job.
- The job runs only for pull requests from this repository, since a fork's token cannot write comments. It needs `pull-requests: write` and `actions: read`, granted to that job only.

Test helpers:

- `internal/testdb`: `testdb.Pool(t)` opens `DATABASE_URL` and applies migrations; `testdb.WaitForJobState` polls a River job until it reaches a state.
- `internal/testbin`: builds the package under test into a real binary and runs it. Each binary is tested this way for its exit code on bad input and, in the integration tests, for a clean exit on SIGTERM. When the coverage script sets `TESTBIN_COVERPKG` and `TESTBIN_COVERDIR`, the binary is built with `-cover` and writes to that directory; the script converts it with `go tool covdata` and merges it into the profile. Without them, the binary is built plainly.

The binaries' `run` functions take a context, so tests also call them in process. Uncovered by design: error branches with no way to trigger them, such as goose or River failing after the connection is up, or the HTTP server failing to shut down.

## LLM

`internal/llm` calls [Vercel AI Gateway](https://vercel.com/docs/ai-gateway/sdks-and-apis/openai-chat-completions) through its OpenAI-compatible `POST /chat/completions` endpoint, using only the standard library. `llm.New(cfg.LLM)` returns an `llm.Client`, whose one method, `Complete`, takes a tier, a system prompt, a user prompt and an optional `MaxOutputTokens`, and returns the text, the model that answered, the finish reason, and the input and output token counts. An empty system prompt is left out of the request, and `MaxOutputTokens` is sent as `max_tokens` only when it is set.

Callers pick a tier, never a model. Config maps each tier to a gateway model ID (`provider/model`), so a model is swapped by changing an environment variable:

| Variable | Default | Meaning |
| --- | --- | --- |
| `AI_GATEWAY_API_KEY` | none | AI Gateway API key, sent as `Authorization: Bearer`. A secret: set it in Railway, never in `.railway/railway.ts`. |
| `AI_GATEWAY_BASE_URL` | `https://ai-gateway.vercel.sh/v1` | Gateway base URL. Must be an `http` or `https` URL with a host, or the binaries fail at startup. |
| `LLM_ROUTINE_MODEL` | `anthropic/claude-haiku-5.5` | Model for the `routine` tier: cheap, fast work such as agenda and transcript summaries. |
| `LLM_STRONG_MODEL` | `anthropic/claude-sonnet-5.5` | Model for the `strong` tier: paid directed prompts and other work that needs a stronger model. |
| `LLM_TIMEOUT` | `2m` | Limit on one gateway call, as a Go duration. Must be positive. |

The current model IDs are listed at `https://ai-gateway.vercel.sh/v1/models`.

Without `AI_GATEWAY_API_KEY` the binaries start normally, and every `Complete` call returns `llm.ErrNotConfigured` without contacting the gateway.

`Complete` does not retry; River retries the job. Errors tell a worker whether a retry can help:

- `llm.IsRetryable(err)` is true for:
  - network failures, the `LLM_TIMEOUT` limit, and a canceled context;
  - 402 (no credit balance, or a budget limit reached), 408, 429, and 5xx;
  - a 200 response that cannot be decoded, has no choices, or has empty text.

  These are `*llm.Error` values. On a 429 with a `Retry-After` header in seconds, `RetryAfter` holds the delay, which a worker can pass to `river.JobSnooze`.
- Every other error is permanent:
  - other 4xx statuses (`StatusCode` on `*llm.Error`, with the gateway's message in the text);
  - `llm.ErrTruncated`: finish reason `length`, so retrying the same input truncates again. Shorten or split the input, or raise `MaxOutputTokens`;
  - `llm.ErrContentFiltered`: finish reason `content_filter`;
  - `llm.ErrResponseTooLarge`: a 200 response over 8 MiB;
  - `llm.ErrNotConfigured`, `llm.ErrUnknownTier`, and an invalid base URL.

  Check for these with `errors.Is`. A worker should wrap them in `river.JobCancel` rather than retry them.

When a reply is truncated, filtered or empty, `Complete` returns the error together with the response, so the model, finish reason and token usage can still be recorded. `llm.CheckReply` applies these reply rules, and the fake uses it too.

A River job's own timeout (one minute unless the worker overrides `Timeout`) also cancels the call, so a worker that calls the gateway should set a `Timeout` longer than `LLM_TIMEOUT`.

Tests never call the gateway. `internal/llm/llmtest` provides `llmtest.Fake`, an `llm.Client` for other packages' tests: `llmtest.Returning(text)` answers every call with `text` and finish reason `stop`, `llmtest.Failing(err)` returns `err`, and a `Respond` function can build a response per request. A zero `Fake` replies `fake reply`. `Calls()` returns the requests received. Like the real client, the fake rejects an unknown tier, returns a retryable `*llm.Error` for a canceled context, and runs `llm.CheckReply` on the responses `Respond` builds. The client's own tests run against an `httptest` server.

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

- **API on Railway**, defined in `.railway/railway.ts`: the `civic-scanner` service and its `Postgres` database in the `production` environment. The service builds `api/Dockerfile` with `/api` as the build context and redeploys only when `api/` changes. The `RAILWAY_GIT_COMMIT_SHA` build arg becomes the version the health endpoint reports.
  - Wait for CI is on: a deploy waits until the GitHub Actions run on that commit finishes.
  - `/app/migrate up` runs as the pre-deploy step, with a 5-minute limit. A failed migration stops the deploy.
  - A deploy goes live only once `/v1/healthz` returns 200 within 60 seconds.
  - `DATABASE_URL` references the database over Railway's private network.
- **Web on Vercel**, with the project root directory set to `web`. Each pull request gets a preview deploy.

### Changing Railway configuration

`.railway/railway.ts` owns the Railway project, including every variable on its services. Change settings there, not in the dashboard: the next apply reverts dashboard edits. Secrets go in as `preserve()` so their values stay out of the repository.

With the [Railway CLI](https://docs.railway.com/cli) installed:

```sh
railway link            # once: pick the project and the production environment
railway config plan     # preview the change; changes nothing
railway config apply    # apply it after reviewing the plan
```

`railway config pull` re-imports the project from Railway. Generated Railway domains are not in the file and are left alone. The file needs no npm install: the CLI evaluates it.
