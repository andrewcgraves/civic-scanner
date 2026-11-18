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

- Go (version in `api/go.mod`). `make lint-api` builds the pinned [golangci-lint](https://golangci-lint.run) into `api/bin/` with that Go version on first use
- Node (version in `web/.nvmrc`) and pnpm (version in `web/package.json`). Node 25 and later no longer bundle Corepack; install pnpm with `npm install --global corepack && corepack enable`, Homebrew, or the standalone installer. pnpm switches itself to the pinned version.
- Docker, for local Postgres

## Quickstart

```sh
make install          # web dependencies
make dev-db           # Postgres 17 on localhost:5432 (db, user, password: civic) and an S3-compatible store on localhost:9090
make migrate          # apply migrations
make test             # unit tests
make prepush          # before every push: linters, unit tests, codegen drift, build (no Docker)
make ci               # everything CI runs
```

`make help` lists every target. The Makefile defaults `DATABASE_URL` to the compose database; override it to point at another database.

The binaries read configuration from the environment only and do not load `.env` files. `api/.env.example` lists the variables (`LOG_LEVEL`: `debug`, `info`, `warn`, `error`; `APP_ENV`: `development`, `production`; the `AI_GATEWAY_*` and `LLM_*` variables are described under "LLM"); copy it to `.env` and export it. For the web app, copy `web/.env.example` to `web/.env.local`; `NEXT_PUBLIC_API_BASE_URL` includes the `/v1` prefix. The flag variables for both sides are described under [Feature flags](#feature-flags).

## OpenAPI-first workflow

1. Edit `openapi/v1.yaml`.
2. Run `make generate` (oapi-codegen for Go, openapi-typescript for TypeScript).
3. Commit the spec and the generated code together.

Generated files are committed and never edited by hand. CI regenerates them and fails on any diff.

## API

Every route is under `/v1`, public and unauthenticated. `openapi/v1.yaml` is the full contract. Errors are JSON `{"code", "message"}` bodies.

| Route | Returns |
| --- | --- |
| `GET /v1/healthz` | `ok`, or `degraded` with 503 when the database is unreachable |
| `GET /v1/agencies` | every live agency |
| `GET /v1/agencies/{slug}` | one live agency, the same shape as a list item |
| `GET /v1/agencies/{slug}/meetings` | one page of the agency's meetings, newest first |

- **Not live is not found:** an unknown slug and a pending or rejected agency get the same 404 (`not_found`), so a caller cannot tell whether an unpublished agency exists. A live agency with no meetings returns an empty list.
- **Meeting items** carry `id`, `title`, `startsAt` (UTC), `status` (`scheduled`, `held`, `cancelled`) and `sourceUrl`, the agency's page for the meeting, left out when the source gave none. New optional fields, such as agenda or summary links, can be added later without breaking clients, which must ignore fields they do not know.
- **Pagination:** keyset, not offset. Meetings are ordered by `starts_at DESC, id DESC`; the id breaks ties between meetings that start at the same time. A page asks for `limit` (default 50, at most 200) and returns `nextCursor`, which is `null` on the last page. Passing it back as `cursor` returns the meetings strictly after the last one on the previous page, so a meeting added or changed between requests never makes another one skipped or repeated. `meetings_agency_starts_at_idx` (`agency_id, starts_at DESC, id DESC`) serves the query.
- **Cursors** are opaque to clients. Inside, a cursor is base64url (no padding) of a small JSON object holding the agency id and the last meeting's start time and id, followed by the first 8 bytes of its SHA-256. The checksum catches cursors that were truncated, edited or corrupted; it is not a secret, because a cursor only names a position in public data. A cursor that does not decode, fails its checksum, or belongs to another agency is a 400 (`invalid_cursor`), never a restart from the first page. A `limit` outside 1 to 200 or not an integer, and any other parameter the router cannot parse, is a 400 (`invalid_parameter`).
- **Order of checks:** a malformed `limit` is rejected before the agency is looked up. The cursor is checked after, since it is tied to the agency, so an unknown agency with a bad cursor is a 404.
- **Caching:** the routes set no `Cache-Control` or validators, the same as `GET /v1/agencies`. A caching policy for the public reads is a separate decision.

## Feature flags

Unfinished work ships dark behind a flag. Both sides evaluate flags through [OpenFeature](https://openfeature.dev), with PostHog as the provider when a key is set and an in-memory list otherwise. Each side has its own variables.

### Web

The web app evaluates every flag on the server, through `@openfeature/server-sdk`, and hands the results to client components. `web/src/lib/flags/` wraps it. Callers import only from there, never from OpenFeature or PostHog.

- **Registry:** every flag is declared in `web/src/lib/flags/registry.ts`. `FLAG_KEYS` lists the keys and `FlagKey` is their union type, so checking a flag that is not registered is a type error. At run time an unregistered flag is off. Add the key there before using it. `test-only-flag` exists only for the module's own tests.
- **Server components:** `await isFlagEnabled("flag-key")` from `@/lib/flags/server`.
- **Client components:** `useFlag("flag-key")` from `@/lib/flags/client`. The root layout calls `getFlagsOn()` from `@/lib/flags/server`, which evaluates each registered key with the OpenFeature client, and passes the keys that are on to `FlagsProvider`, a plain React context that supplies `useFlag`. A client component's server-rendered HTML and its first render in the browser therefore already show the right value: a flag that is on does not flash off, and hydration does not mismatch.

Every flag is a boolean and defaults to off. An unregistered or unknown flag, a multivariate PostHog flag, a PostHog error, a slow or unreachable PostHog, a provider that fails to start, and a client component outside `FlagsProvider` all evaluate to off.

The provider depends on the environment:

| Variable | Meaning |
| --- | --- |
| `NEXT_PUBLIC_POSTHOG_KEY` | PostHog project API key. When set, PostHog serves flags and `NEXT_PUBLIC_FEATURE_FLAGS` is ignored. |
| `NEXT_PUBLIC_POSTHOG_HOST` | PostHog API host. Defaults to `https://us.i.posthog.com`. |
| `NEXT_PUBLIC_FEATURE_FLAGS` | Used when there is no PostHog key: a comma-separated list of flags that are on, or `*` for every registered flag. Unset or empty means all off. For local development, tests, and preview deploys. |

Notes:

- **Swapping vendors:** `createProvider` in `web/src/lib/flags/server.ts` is the only place a provider is chosen, and it is registered with OpenFeature once per process. Moving to another vendor, such as LaunchDarkly, means returning that vendor's OpenFeature server provider from `createProvider`. Callers, the registry and `client.tsx` do not change.
- `NEXT_PUBLIC_` values are inlined at `next build`, so changing one needs a new build. On Vercel, set them per environment: the PostHog key for Production, and `NEXT_PUBLIC_FEATURE_FLAGS` for Preview. Only server code reads them.
- The PostHog provider is ours, `PostHogProvider` in `web/src/lib/flags/posthog.ts`, a thin OpenFeature provider over `posthog-node`. It does not use PostHog's `@posthog/openfeature-node-provider`, which sends one `/flags` request per flag and caches nothing. Ours asks `evaluateFlags` once for every flag of the identity, keeps the answer for 30 seconds per process, and serves each boolean evaluation from it. So the layout and any number of `isFlagEnabled` calls cost at most one PostHog request per process every 30 seconds, and a change in PostHog can take that long to show. The request times out after 500 ms and is not retried. If it fails, every flag is off until the cache entry expires, and the error is logged as `Feature flags could not be evaluated`. Nothing else is sent to PostHog: no `$feature_flag_called` events, page views, or other analytics. Analytics is a separate change.
- The in-memory list is an OpenFeature provider too (`web/src/lib/flags/memory.ts`), so both modes go through the same OpenFeature client.
- Within one request, `getFlagsOn` is memoised with React's `cache`, so the layout, server components and client components see the same values even if the 30-second entry expires mid-render.
- Rendering: in PostHog mode `getFlagsOn` calls `connection()` before asking PostHog, and the root layout calls `getFlagsOn`, so every page is rendered on each request, including pages that check no flag. That is the price of client components getting PostHog values in their first render, and it keeps a change in PostHog live without a deploy. It adds a server render per page view, not a PostHog request. In in-memory mode `getFlagsOn` does not call `connection()`, so pages stay static.
- Identity: there is no sign-in yet, and the app stores no visitor identifier, so the server evaluates every flag for one anonymous distinct ID, `anonymous`, and the browser uses the server's values. Both sides always agree, but a percentage rollout or person targeting is on or off for everyone. Per-user flags later mean evaluating for the user's distinct ID per request instead of reading the shared cache, and passing the result through the same `FlagsProvider`; callers do not change.
- The browser loads no PostHog or OpenFeature code and makes no `/flags` request. The layout sends the registered keys that are on, and only those, in the page payload, so anyone can read them in the page source: a flag key must not be secret.
- Navigating between pages in the browser does not render the root layout again, so client components keep the values from the last full page load, while server components on the new page read current values. After a flag changes in PostHog, the two can disagree until the visitor reloads.
- The flags module picks its provider once, when it loads. A test sets `NEXT_PUBLIC_FEATURE_FLAGS` with `vi.stubEnv`, calls `vi.resetModules()`, and imports the module again; see `web/src/lib/flags/*.test.*` and `web/src/app/layout.test.tsx`. Tests never call PostHog: the PostHog cases stub `fetch`, which `posthog-node` uses.

### API

The API and the worker evaluate flags through OpenFeature's Go SDK. Handlers and jobs call `featureflags.Enabled(ctx, "new-search")` from `internal/featureflags` and never import a vendor SDK.

Both binaries call `featureflags.Init` at startup, which picks the provider from these variables:

| Variable | Meaning |
| --- | --- |
| `POSTHOG_API_KEY` | PostHog project API key. When set, PostHog decides every flag and `FEATURE_FLAGS` is ignored. |
| `POSTHOG_HOST` | PostHog host. Empty means `https://us.i.posthog.com`; EU projects use `https://eu.i.posthog.com`. With a PostHog key set, anything other than an `http` or `https` URL with a host (for example `us.i.posthog.com` without a scheme) stops the binary at startup. |
| `POSTHOG_FLAG_TIMEOUT` | How long one PostHog request for flags may take, as a Go duration such as `500ms`. Default `1s`. A value that does not parse, or is not positive, stops the binary at startup. |
| `POSTHOG_FLAG_CACHE_TTL` | How long the provider reuses one PostHog answer, as a Go duration such as `10s`. Default `30s`. `0` turns the cache off, so every check is a request. A value that does not parse, or is negative, stops the binary at startup. |
| `FEATURE_FLAGS` | Used only without a PostHog key. A comma-separated list of the flags that are on, such as `new-search,digest-emails`. `*` turns every flag on, for staging-like runs. Empty means every flag is off. |

- A flag is off unless the provider says it is on. These all evaluate to off: an unknown flag, a PostHog error, an unreachable or slow PostHog, a flag PostHog failed to evaluate, a quota-limited project, and a multivariate flag (whatever its variant, since `Enabled` answers only boolean flags).
- What gets logged at `warn`: request errors, timeouts, context deadlines and multivariate flags, all by the provider. posthog-go logs quota limits and responses that report `errorsWhileComputingFlags` itself, through the service logger. posthog-go returns the same `ErrFlagNotFound` for an unknown flag, a flag that failed to evaluate and a quota-limited project, and does not expose which one happened. So the provider logs none of those three itself, and a failed flag is logged only when PostHog also reports `errorsWhileComputingFlags`.
- PostHog evaluates flags remotely, through its `/flags` endpoint, and one response holds every flag for one identity. The provider caches that whole response per identity for `POSTHOG_FLAG_CACHE_TTL`, so repeated checks of any flag make no request. The identity is the OpenFeature targeting key, or `civic-scanner-server` when a caller passes none, so a percentage rollout gives the same answer to every call that has no targeting key. A change in PostHog can take up to the TTL to show.
- At most one request per identity is in flight: checks that arrive while it runs wait for that answer. The request runs on its own, so a caller whose context ends stops waiting and gets off, and the request still finishes and fills the cache for the next check. One attempt, no retries, limited by `POSTHOG_FLAG_TIMEOUT`. posthog-go does not take a context, so this is how the caller's deadline is honoured.
- The cache holds at most 1024 identities. Adding one past that evicts the identity checked longest ago.
- When a request fails, the provider serves the last good answer for that identity while it is at most 5 times the TTL old (150 s by default), then evaluates every flag to off. A flag that was never fetched is off at once. Failures are not retried within one TTL, so an outage costs one request per identity per TTL, not one per check. Off after a bounded grace period, not stale forever, because a rollout that was turned off in PostHog must not stay on indefinitely behind an outage; a short grace keeps a brief PostHog blip from flipping every flag. Each failed request is logged once at `warn`.
- With `POSTHOG_FLAG_CACHE_TTL=0` there is no cache, no sharing and no grace: every check makes its own request. No `$feature_flag_called` events are sent.
- Not done: PostHog local evaluation, where the client downloads flag definitions and evaluates them in process. It needs a personal API key, a secret that can read the whole project, in every binary. The cache gets the call volume down without one.
- The PostHog provider (`internal/featureflags/posthog.go`) is a thin layer over the official `posthog-go` client, because no maintained OpenFeature Go provider for PostHog exists. Another vendor means another provider in that package; callers do not change.
- OpenFeature's provider is process-wide. A test that calls `featureflags.Init` with `config.FeatureFlags{On: ...}` must not run in parallel with other flag tests. Parallel tests can register OpenFeature's `openfeature/testing` provider instead, which keys flags by test.

## CI

`.github/workflows/ci.yml` runs on every pull request and on pushes to `main`. Jobs run only when their paths change; a spec change runs both `api` and `web`.

| Job | Checks |
| --- | --- |
| `openapi-lint` | Redocly lint of `openapi/*.yaml` |
| `api` | build, vet, golangci-lint, unit tests with `-race` and coverage, codegen drift |
| `api-integration` | unit and integration tests with `-race` and coverage, against Postgres 17 and S3Mock containers; fails below the coverage minimum |
| `migrations-lint` | Squawk on changed migrations |
| `migrations-compat` | pull requests that change `api/migrations/**`: applies the migrations, then runs the base branch's integration tests against that database, with Postgres 17 and S3Mock containers like `api-integration` |
| `web` | lint, format, typecheck, tests with coverage, codegen drift, build |
| `coverage-comment` | posts or updates the coverage comment on the pull request; not part of `ci-ok` |
| `ci-ok` | passes when none of the above failed; the one required check on `main` |

Branch rules for `main` are in `.github/rulesets/` (import instructions there).

Notes on the workflow:

- Path filtering lives in the `changes` job, not in `on.paths`, so the workflow always starts and `ci-ok` always reports. Skipped jobs count as passing; failed or cancelled jobs fail it.
- A new push to a pull request cancels the run for the previous push. Runs on `main` are keyed by commit and never cancelled.
- `migrations-lint` lints the migrations added or modified by the change. If none were (only the linter config or the workflow changed), it lints all of them.
- Tool versions (Redocly CLI, Squawk, golangci-lint) and `GO_COVERAGE_MIN` are pinned in the workflow `env` and mirrored in the `Makefile` and `.github/scripts/lint-migrations.sh`. Bump them together.
- `migrations-compat` runs `.github/scripts/check-migration-compat.sh`; see "Backward compatibility check" under Migrations. It runs only on pull requests that change `api/migrations/**` (a change to only the workflow or the script does not trigger it), never on pushes to `main`. A skipped job counts as passing in `ci-ok`; a failure fails it.

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

## Object storage

`internal/storage` stores the audio track of meeting recordings in a [Railway bucket](https://docs.railway.com/storage-buckets), which speaks the S3 API. `storage.New(cfg.Storage)` returns a `storage.Store` with five methods: `Put` (streams from an `io.Reader`), `Get` (returns a streaming `io.ReadCloser`), `Delete` (a missing key is not an error), `Stat` (key, size, last modified) and `ListOlderThan` (keys last modified before a time, for the retention job). Keys are chosen by the caller. Nothing calls the store yet.

It uses [`minio-go`](https://github.com/minio/minio-go) rather than the AWS SDK for Go v2: it takes any S3-compatible endpoint, streams an unsized `io.Reader` as a multipart upload without a seekable body, switches between path-style and virtual-hosted-style addressing with one option, and pulls in far fewer dependencies.

Errors:

- `storage.ErrNotFound` marks a missing key (from `Get` and `Stat`); test with `errors.Is`.
- `storage.ErrNotConfigured` comes from every method when none of the variables below is set. The binaries still start.
- `storage.IsRetryable(err)` is true for network errors, cancelled contexts, HTTP 408, 429 and 5xx, so a River job can return the error and retry. Everything else (bad credentials, missing bucket) is permanent. The client itself retries a failed request twice.

Tests of code that uses a store use `storage/storagetest`: `storagetest.New()` is an in-memory store with a settable clock (`Now`), and `storagetest.Failing(err)` fails every operation with `err`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `STORAGE_ENDPOINT` | | S3 endpoint as `http(s)://host[:port]`, no path. Unset (with the rest) means not configured. |
| `STORAGE_REGION` | `auto` | Signing region. Railway buckets use `auto`. |
| `STORAGE_BUCKET` | | Bucket name for the S3 API (Railway's `BUCKET`, not `RAILWAY_BUCKET_NAME`) |
| `STORAGE_ACCESS_KEY_ID` | | Access key |
| `STORAGE_SECRET_ACCESS_KEY` | | Secret key |
| `STORAGE_PATH_STYLE` | `false` | Use path-style URLs. Railway buckets use virtual-hosted-style, except ones created long ago, whose Credentials tab says so. |

Setting any of the variables without a valid endpoint, bucket, access key and secret makes `storage.New` fail.

In production the `audio` bucket is defined in `.railway/railway.ts` and the API service's `STORAGE_*` variables reference it. Objects are meant to be deleted after about six months; the retention job that does it is not built yet, and Railway buckets have no lifecycle rules.

Locally, `make dev-db` also starts [Adobe S3Mock](https://github.com/adobe/S3Mock) on `localhost:9090` with a `civic-audio` bucket (the image is small, needs no start arguments so it works as a CI service container, and accepts any credentials). The Makefile exports matching `STORAGE_*` defaults, which `make test-integration` and `make coverage` use. The `api-integration` CI job runs the same image as a service container.

## Fetching

`internal/fetch` is the one way source adapters and the agency screener read a public page, feed or document. A `fetch.Fetcher` has one method, `Fetch(ctx, fetch.Request{URL, MaxBytes})`, and returns a `fetch.Response` with `Status`, `ContentType`, `Body` and `FinalURL` (after redirects). Only `http` and `https` URLs are accepted.

- `fetch.NewPlain(cfg.Fetch)` makes a plain HTTP GET.
- `fetch.NewFirecrawl(cfg.Fetch)` calls the [Firecrawl](https://docs.firecrawl.dev/api-reference/endpoint/scrape) `POST /v2/scrape` endpoint with `Authorization: Bearer` and returns the page as markdown (content type `text/markdown`). It always asks for a live fetch (`maxAge: 0`), not Firecrawl's two-day cache.
- `fetch.New(cfg.Fetch)` is the composition callers should use: plain HTTP first, then Firecrawl when the site refuses.

`MaxBytes` caps one call's body and overrides `FETCH_MAX_BYTES` when set, so a caller reading agenda PDFs can raise it. A larger body returns `fetch.ErrTooLarge`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `FETCH_USER_AGENT` | `CivicScanner/1.0 (+https://github.com/andrewcgraves/civic-scanner)` | User-Agent for plain HTTP. Names the service and gives a contact URL. Set it to a URL that reaches a person who answers. |
| `FETCH_TIMEOUT` | `30s` | Limit on one plain HTTP call, including redirects and reading the body. Must be positive. |
| `FETCH_MAX_BYTES` | `134217728` | Default response size cap in bytes (128 MiB). Must be positive. |
| `FETCH_MAX_REDIRECTS` | `5` | Redirects followed before `fetch.ErrTooManyRedirects`. Must be positive. |
| `FIRECRAWL_API_KEY` | none | Firecrawl API key. A secret: set it in Railway, never in `.railway/railway.ts`. |
| `FIRECRAWL_BASE_URL` | `https://api.firecrawl.dev` | Firecrawl base URL. |
| `FIRECRAWL_TIMEOUT` | `90s` | Limit on one Firecrawl call. Must be positive. |

Several source hosts disallow crawlers in `robots.txt`. We identify ourselves honestly instead of hiding: plain HTTP sends `FETCH_USER_AGENT` on every request, including each redirect hop. Requests that go through Firecrawl do not carry our User-Agent. Firecrawl fetches from its own infrastructure with its own browser identity, so the host sees Firecrawl, not Civic Scanner.

Without `FIRECRAWL_API_KEY` the binaries start normally, and the Firecrawl fetcher returns `fetch.ErrNotConfigured` when used.

The fallback tries plain HTTP and calls Firecrawl only when the plain error carries one of these statuses, which sites use to turn away automated clients: 401, 403, 406, 429 and 999 (the non-standard code some sites return to bots). Other failures are not bot blocks and Firecrawl would not fix them: 404 and 410 mean the page is gone, 5xx and network errors are transient and River should retry the plain fetch, and too-large and redirect-loop errors would repeat. If Firecrawl is not configured, the fallback returns the original plain HTTP error. If Firecrawl is configured and also fails, its error is returned.

Errors are `*fetch.Error` values, and `fetch.IsRetryable(err)` tells a worker whether a retry can help, as in `internal/llm`:

- Retryable: network failures, timeouts, a canceled context, 408, 429 and 5xx, and a Firecrawl 200 response that cannot be decoded or reports failure. On a 429 with a `Retry-After` header in seconds, `RetryAfter` holds the delay, which a worker can pass to `river.JobSnooze`. A target page's own status through Firecrawl is classified the same way.
- Permanent: other 4xx statuses (`StatusCode` on `*fetch.Error`), `fetch.ErrTooLarge`, `fetch.ErrTooManyRedirects`, `fetch.ErrInvalidURL`, and `fetch.ErrNotConfigured`. A worker should wrap these in `river.JobCancel` rather than retry them.

Tests never call a live site or Firecrawl. `internal/fetch/fetchtest` provides `fetchtest.Fake`, a `fetch.Fetcher` for other packages' tests: `fetchtest.Returning(body)` answers every call with a 200 HTML page, `fetchtest.Failing(err)` returns `err`, and a `Respond` function can build a response per request. `Calls()` returns the requests received. The fetchers' own tests run against `httptest` servers.

## Ingestion

The worker checks every live agency once a day for board meetings and records them. This is the first link of the pipeline (check agency, process agenda, download audio, transcribe, summarize). Two River jobs in `internal/jobs` do it, behind the `ingest-check-agencies` feature flag.

| Variable | Default | Meaning |
| --- | --- | --- |
| `INGEST_CHECK_HOUR_UTC` | `13` | Hour of the day, in UTC, when the daily check runs (13:00 UTC is 05:00 in Los Angeles in winter, 06:00 in summer). An integer from 0 to 23; anything else stops the worker at startup. |

### Jobs

- `enqueue_agency_checks` is a River periodic job. It runs every day at `INGEST_CHECK_HOUR_UTC`:00 UTC, and also when the worker starts, so a run missed while the worker was down happens on the next start. Its args carry the slot it belongs to: the latest scheduled time at or before now. It is unique by slot, so a restart, a deploy or a new River leader inserts it at most once per day. It inserts one `check_agency` job per live agency (`store.ListAgencies`); pending and rejected agencies get none.
- `check_agency` checks one agency. It is unique by agency slug, queue and UTC day (River `UniqueOpts` `ByArgs`, `ByQueue`, `ByPeriod` of 24 hours), and the enqueuer sets its scheduled time to the slot, so a second enqueue in the same day is skipped as a duplicate. River keeps completed jobs for 24 hours, which is what lets a completed check block a duplicate. The job has a 10 minute timeout, for slow agency sites, and at most 5 attempts.

`check_agency` does this, in order:

1. With the flag off, it completes without doing anything. With the flag off, `enqueue_agency_checks` also inserts nothing. Flags are off unless turned on (see [Feature flags](#feature-flags)).
2. It looks up the agency's source adapter by slug. An agency with no adapter is skipped: an info log, and the job completes. So is an agency that is no longer live.
3. It asks the adapter for meetings in the window from 30 days before now to 90 days after. The look-back catches recordings, minutes and status changes posted after a meeting; the look-ahead covers the meeting calendars agencies publish for the coming quarter.
4. It checks the result with `sources.Validate`. A result that breaks the adapter contract cancels the job and writes nothing.
5. In one transaction for the agency, it upserts each meeting with `store.UpsertMeetingTx` (unique on agency and source id, so a rerun updates rows instead of duplicating them; a changed title or start time updates the row) and hands each one to the follow-up hook. Any error rolls back the whole agency.

Errors:

- Retryable: `sources.Retryable(err)`, which is `fetch.IsRetryable` (network failures, timeouts, 408, 429, 5xx) plus a canceled context or exceeded deadline, including the job's own timeout. The job returns the error and River retries it with backoff. Database and follow-up errors are retried the same way.
- Permanent: everything else, such as a 404 or other 4xx, `fetch.ErrTooLarge`, markup the adapter cannot parse, or `sources.ErrInvalidMeeting`. The job is cancelled with `river.JobCancel`; the next day's run tries again.

One agency's failure never affects another: each agency is its own job and its own transaction.

Meeting status: `held` is final. Once a meeting is `held`, the upsert keeps it `held` whatever a source reports later, so a source that misreports a past meeting as `scheduled` cannot undo it. Other changes follow the source: `scheduled` to `cancelled`, `cancelled` back to `scheduled` (reinstated), and either to `held`. The rule lives in the upsert's SQL, so every caller gets it.

### Source adapters

Each agency's site is read by a source adapter in `internal/sources`:

```go
type Adapter interface {
	Meetings(ctx context.Context, window Window) ([]Meeting, error)
}

type Window struct{ From, To time.Time }

type Meeting struct {
	SourceID     string
	Title        string
	StartsAt     time.Time
	Status       Status
	PageURL      string
	AgendaURL    string
	RecordingURL string
}
```

`Status` is `sources.StatusScheduled`, `StatusHeld` or `StatusCancelled`, the values the `meetings` table accepts.

Adapters are registered by agency slug in `cmd/worker/adapters.go`, a map from slug to `sources.Constructor` (`func(sources.Deps) sources.Adapter`). At startup the worker builds the registry with `sources.NewRegistry(deps, adapters)`, which calls each constructor once with `sources.Deps{Fetcher, Clock}`: `fetch.New(cfg.Fetch)` and `sources.SystemClock{}`. Adapters get their dependencies only from `Deps`, never from globals. Registered today: `la-metro` (the Legistar adapter below) and `metrolink` (the Granicus adapter below). Every other agency is skipped until its adapter lands. The worker logs the registered slugs when it starts.

An adapter must guarantee:

- **Board only.** It returns meetings of the agency's full governing board, including special board meetings, and never committees, subcommittees, advisory groups or workshops of part of the board.
- **Stable ids.** `SourceID` is the source's own identifier for the meeting (a Legistar event id, a PrimeGov meeting id, a page URL path), the same on every run and never reused for another meeting. Never derive it from the title or the time, which change. Ids are unique within one result.
- **Idempotent results.** The same source state gives the same result. `Meetings` has no side effects beyond fetching, keeps no state between calls, and is safe to call again after a failure.
- **Times in UTC.** `StartsAt` is the correct instant, with location `time.UTC`. Parse the source's wall-clock time in the agency's zone (`America/Los_Angeles`) and call `.UTC()`; `sources.Validate` rejects any other location.
- **Status.** `held` once the source shows the meeting took place (minutes, a recording, or a past date the source marks as held), `cancelled` when the source says so, `scheduled` otherwise.
- **URLs.** `PageURL` is the meeting's page and is required. `AgendaURL` is the agenda document once it is published and `RecordingURL` the recording once it is available; each is empty until then. Every URL is absolute `http` or `https`.
- **Window.** It returns the board meetings starting within the window. Extra meetings outside it are stored too, so returning a whole page of a calendar is fine.
- **HTTP through the fetcher.** Every request goes through `Deps.Fetcher`, and time comes from `Deps.Clock`. Respect `ctx`.
- **Errors.** Return the fetcher's errors wrapped with `%w`, so `sources.Retryable` can classify them; do not retry inside the adapter, since River does. Any other error, such as markup that no longer parses, is permanent.

Tests never call a live site. `internal/sources/sourcestest` has a fake adapter for other packages' tests: `sourcestest.Returning(meetings...)` answers every call with those meetings, `sourcestest.Failing(err)` returns `err`, `Set` and `Fail` change the answer between runs, `Calls()` returns the windows received, and `sourcestest.Constructor(a)` registers it. `sourcestest.NewClock(t)` is a fake clock with `Set` and `Advance`. An adapter's own tests feed it `fetchtest.Fake` responses and check its result with `sources.Validate`.

### Legistar

`internal/sources/legistar` reads any agency that publishes through the [Legistar Web API](https://webapi.legistar.com/Help) (`https://webapi.legistar.com/v1/{client}/events`). One agency is one `legistar.Config`:

```go
var LAMetro = legistar.Config{
	Client:   "metro",
	BodyIDs:  []int{138, 199},
	TimeZone: "America/Los_Angeles",
	Granicus: legistar.Granicus{Host: "metro.granicus.com", ViewID: 2},
}
```

| Field | Meaning |
| --- | --- |
| `Client` | The Legistar client name, the path segment after `/v1/`. |
| `BodyIDs` | The `EventBodyId`s of the full board: regular and special board meetings, no committees. |
| `TimeZone` | IANA zone of the agency's wall-clock times. |
| `Granicus` | Host and `view_id` of the agency's Granicus media player, for the recording URL. Leave it empty if the agency has none. |
| `BaseURL` | Defaults to `https://webapi.legistar.com`. Tests point it elsewhere. |
| `PageSize` | Defaults to 1000, Legistar's largest page. |

To add a Legistar agency:

1. List its bodies: `GET https://webapi.legistar.com/v1/{client}/bodies`. Pick the full board's ids by name, not by `BodyTypeName`: "Primary Legislative Body" also covers test bodies, workshops and public hearings. Skip the `(SAP)` bodies, which are Spanish audio copies of the same meetings.
2. Add a `Config` next to `LAMetro` in `internal/sources/legistar/agencies.go`, and register `legistar.Constructor(cfg)` under the agency's slug in `cmd/worker/adapters.go`. `Constructor` panics on an invalid config, so a bad config stops the worker at startup; `legistar.New` returns the error instead.
3. Record one real events response (step 1's GET, with our User-Agent), trim it, save it under `internal/sources/legistar/testdata/`, and add a test like `TestLAMetroBoardMeetingsFromRecordedResponse`.

For LA Metro, body 138 is "Board of Directors - Regular Board Meeting" and 199 "Board of Directors - Special Board Meeting". Not included: the committees, the `(SAP)` copies, the service councils, the taxpayer oversight committees, the public hearings, the test bodies (300 to 303), and the board workshops ("Special Board Budget Workshop" 306, "Special Board Member Workshop" 310).

How the adapter reads Legistar:

- **Query.** One `GET /v1/{client}/events` with `$filter=EventDate ge datetime'{from}' and EventDate le datetime'{to}' and (EventBodyId eq … or …)`, `$orderby=EventDate,EventId`, `$top` and `$skip`. `{from}` and `{to}` are the window's ends as dates in the agency's zone, since `EventDate` is a local date. It pages with `$skip` until a page is shorter than `$top`; an event repeated across pages is returned once, and a full page with no new events is a malformed response. Events of other bodies are dropped in code as well.
- **Ids and titles.** `SourceID` is `EventId`. `Title` is `EventBodyName`.
- **Time.** `EventDate` holds the local date (its time part is always midnight) and `EventTime` the local wall-clock time as text, such as `10:00 AM`. The two are joined in the agency's zone and converted to UTC, so daylight saving time is applied for that date. Accepted time forms are `3:04 PM`, `3:04PM` and `15:04`, in either case. A missing, empty or unparseable `EventTime` gives local midnight of the date: the date is right, and when the agency fixes the time the next check moves the meeting.
- **Status.** `cancelled` when `EventAgendaStatusName` contains "cancel" in any case (LA Metro writes `Canceled`, OCTA `CANCELLED`). Otherwise `held` when the start time has passed and there is evidence the meeting took place: a recording (`EventMedia` or `EventVideoPath`), a minutes file (`EventMinutesFile`), or minutes status `Final`. Otherwise `scheduled`, which includes a past meeting with no such evidence. A past date alone is not evidence: LA Metro's minutes stay `Draft` with no file in Legistar, so for LA Metro the recording is the evidence.
- **URLs.** `PageURL` is `EventInSiteURL`. `AgendaURL` is `EventAgendaFile`, empty until the agenda is published. `RecordingURL` is `EventVideoPath` when set; otherwise, when `EventMedia` (a Granicus clip id) is set and `Granicus` is configured, `https://{host}/MediaPlayer.php?view_id={view}&clip_id={EventMedia}`; otherwise empty. The adapter never fetches the player page or the media; resolving the media file belongs to the recording download.
- **Errors.** Fetch errors are wrapped with `%w` and keep their retryability. A body that is not a JSON list of events, or an event without an id, a parseable date, a body name, an absolute `EventInSiteURL`, or with a relative agenda or video URL, is `legistar.ErrMalformed`, which is permanent. If Legistar answers 401, 403, 406 or 429 and Firecrawl is configured, `fetch.New` falls back to Firecrawl, whose markdown is not JSON, so that check is cancelled as malformed and the next day's check tries again.

The daily check calls only the API host, which has no `robots.txt`. The agenda host `metro.legistar1.com` and the player host `metro.granicus.com` disallow crawlers. The check does not touch them; the later stages that do identify themselves with `FETCH_USER_AGENT` (see [Fetching](#fetching)).

The fixtures in `internal/sources/legistar/testdata` are real LA Metro responses recorded on 2026-11-17 and trimmed to a few events and fields: `metro_board_events.json` (the board-only query for 2026-01-01 to 2027-01-31) and `metro_events_with_committee.json` (a board meeting and a Planning and Programming Committee meeting from an unfiltered query). LA Metro had no upcoming board meeting without an agenda that day, so that case, the time edge cases and the status edge cases use events built in the tests.

### Granicus

`internal/sources/granicus` reads any agency that publishes its meetings through a Granicus `ViewPublisher` page, from that view's two RSS feeds: `https://{host}/ViewPublisherRSS.php?view_id={view}&mode=agendas` and `&mode=vod`. One agency is one `granicus.Config`:

```go
var Metrolink = granicus.Config{
	Host:       "metrolink.granicus.com",
	ViewID:     8,
	BoardNames: []string{"Board of Directors", "Special Board of Directors", "Special Board Meeting", "Board Meeting"},
	TimeZone:   "America/Los_Angeles",
}
```

| Field | Meaning |
| --- | --- |
| `Host` | The agency's Granicus host, with no scheme, port or path. |
| `ViewID` | The `view_id` of the agency's publisher page. |
| `BoardNames` | Meeting names of the full board, matched whole and without regard to case or repeated spaces. Everything else (committees, workshops, single-item attachments) is dropped. |
| `TimeZone` | IANA zone of the agency's wall-clock times. |

To add a Granicus agency, find the `view_id` in the `ViewPublisher.php` iframe on its board page, list the meeting names in its agenda feed, add a `Config` next to `Metrolink` in `internal/sources/granicus/agencies.go`, register `granicus.Constructor(cfg)` under the agency's slug in `cmd/worker/adapters.go`, and record both feeds as fixtures. `Constructor` panics on an invalid config; `granicus.New` returns the error.

How the adapter reads Granicus:

- **Feeds.** Two GETs per check: the agenda feed, then the video feed. Each lists the view's latest 100 archived clips, newest first, with no date filter, so the adapter keeps the board clips whose start falls in the window. A clip in both feeds is one meeting.
- **Ids and titles.** `SourceID` is the `clip_id` from the item's link, which the agenda and video feeds share. The RSS `guid` is not used. An item title is `{name} - {Mon DD, YYYY}`; the name, without the date, is the `Title` and is matched against `BoardNames`.
- **Time.** From `gran:pubDateParts` (`yr`, `mo`, `day`, `hr`, `min`), read as wall-clock time in the agency's zone and converted to UTC. The RSS `pubDate` is not used: its offset is always `-0800` and its hour is sometimes on a 12-hour clock (Metrolink's 2:30 PM meeting on 2026-10-09 has `02:30:00 -0800`).
- **Status.** `cancelled` when the name ends in a cancel marker such as `- CANCELLED` or `(Cancelled)` (the marker is removed before matching the name). Otherwise `held` when the start time has passed and the clip is in the video feed. Otherwise `scheduled`.
- **URLs.** `AgendaURL` is the agenda feed's link, `https://{host}/AgendaViewer.php?view_id={view}&clip_id={clip}`, which redirects to the agenda PDF. `RecordingURL` is the video feed's link, `https://{host}/MediaPlayer.php?view_id={view}&clip_id={clip}`. `PageURL` is the player page when there is one and the agenda link otherwise. The adapter never fetches the agenda, the player page or the media. The player page holds the media file's uuid, which can differ from the RSS `guid`, so the recording download must read it from that page.
- **Errors.** Fetch errors are wrapped with `%w` and keep their retryability. A body that is not RSS with a channel, or a board item whose link is not an absolute URL with a numeric `clip_id`, or whose `pubDateParts` is missing or not a real date and time, is `granicus.ErrMalformed`, which is permanent. Items with other names are dropped before they are checked. A Firecrawl fallback answer (markdown) is malformed in the same way as for Legistar.

Limits: the feeds hold archived clips only. Granicus lists upcoming meetings separately on the `ViewPublisher` page under `event_id`, a different id from the clip's, so they are not read: reading both would store one meeting twice. A meeting appears once Granicus archives its clip, so it is not known in advance; how soon after the meeting that happens has not been measured. On 2026-11-18 Metrolink's page listed no upcoming events.

`metrolink.granicus.com` serves `User-agent: * / Disallow: /` in `robots.txt`. Following the decision in CIV-62, the daily check fetches the two feeds once a day with `FETCH_USER_AGENT` (see [Fetching](#fetching)).

The fixtures in `internal/sources/granicus/testdata` are the real Metrolink feeds recorded on 2026-11-18 and trimmed to their first six items: five Board of Directors meetings (2026-07-24 to 2026-11-13) and one Budget, Audit, and Finance Committee meeting. Both feeds list the same clips. Upcoming, cancelled and agenda-only meetings, and the malformed cases, use feeds built in the tests.

### Follow-up jobs

The next stories enqueue `process-agenda` when a meeting has an agenda URL and `download-audio` when it has a recording URL, in the same transaction as the upsert. The seam is `jobs.FollowUp`:

```go
type FollowUp interface {
	MeetingUpserted(ctx context.Context, tx pgx.Tx, m UpsertedMeeting) error
}

type UpsertedMeeting struct {
	Meeting      store.Meeting
	Created      bool
	AgendaURL    string
	RecordingURL string
}
```

`check_agency` calls it for every upserted meeting, inside the upsert transaction; an error rolls back the agency's whole check. Today `jobs.NoFollowUp` does nothing. An implementation inserts its jobs with `river.ClientFromContext[pgx.Tx](ctx).InsertTx(ctx, tx, ...)` and is set through `jobs.Config.FollowUp`.

### Tests

The unit tests use the fake adapter and fake clock. The integration tests run a real River client against Postgres. Each test uses its own random queue (`jobs.Config.Queue`) and turns off the periodic job (`jobs.Config.NoPeriodicJobs`), so tests in other packages, which share the database, never work its jobs. A test that runs a check twice schedules the runs on different days, since a second check the same day is a duplicate.

## Agendas

A meeting's agenda is kept as numbered versions in `agenda_versions` (migration `00004`). Each row belongs to one meeting and holds the source document URL, the extracted text, a content hash, a nullable AI summary, a nullable change note, and `created_at`/`updated_at`. `version` starts at 1 for each meeting and is unique per meeting. The latest version, the highest number, is the one shown. Versions are never deleted or rewritten; only the summary and the change note are set after the insert.

`internal/store/agendas.go`:

| Function | Does |
| --- | --- |
| `AddAgendaVersion(ctx, in)`, `AddAgendaVersionTx(ctx, tx, in)` | Adds a version from `AgendaVersionInput{MeetingID, SourceURL, Text, ContentHash}`. When the meeting's latest version has the same hash, it adds nothing and returns that version with `created` false; otherwise it inserts the next number and returns `created` true. A missing meeting is `ErrNotFound`. |
| `LatestAgendaVersion(ctx, meetingID)` | The highest-numbered version, or `ErrNotFound` when the meeting has none. |
| `ListAgendaVersions(ctx, meetingID)` | Every version, newest first. Empty for a meeting with none. |
| `SetAgendaSummary`, `SetAgendaChangeNote` (and `...Tx`) | Sets the field on one version by id and returns the updated version. A missing version is `ErrNotFound`. |

The `...Tx` variants run in the caller's transaction, so a job can add a version and enqueue its follow-up River job atomically.

**Content hash.** The caller chooses what is hashed; the store chooses how. `store.ContentHash` can only be made with `store.HashContent(s)`, which returns the SHA-256 of `s` as lowercase hex, so a caller cannot pass an arbitrary string, a different algorithm or a hash of the wrong thing by accident. Its zero value is rejected with `ErrZeroContentHash` before the database is touched, and the column's check constraint accepts only 64 lowercase hex characters. Hash the text in the form that decides whether the agenda changed (for example, the normalized text, so a whitespace-only change is not a new version) and store the extracted text as `Text`. Compare hashes with `==`; `String()` gives the hex.

**Concurrency.** Two jobs may add a version for the same meeting at once. `AddAgendaVersion` locks the meeting row with `SELECT ... FOR NO KEY UPDATE` before it reads the latest version, then inserts. Adds for one meeting run one at a time; adds for different meetings never wait for each other. `FOR NO KEY UPDATE` does not block inserts elsewhere that only reference the meeting by foreign key. The lock is held until the transaction ends, which for the `...Tx` variants is when the caller commits or rolls back, so keep that transaction short. It also waits on a concurrent `UpsertMeetingTx` of the same meeting. The `...Tx` variants need the transaction at `READ COMMITTED`, Postgres's default and what `pool.Begin` gives: under `REPEATABLE READ` the waiting transaction would not see the version committed ahead of it, and the unique constraint on `(meeting_id, version)` would reject its insert instead of silently duplicating a number. `TestAddAgendaVersionConcurrentAddsGetDistinctNumbers` runs concurrent adds, through both variants, and checks the numbers are 1 to N with none lost; without the lock it fails with unique violations.

The migration's down half drops the table. `api/migrations/down_integration_test.go` applies every migration in a throwaway schema, rolls back to `00003`, checks the table is gone, and applies it again, without touching the shared tables other tests use.

## Transcription

`internal/transcribe` turns an audio file into a timestamped transcript through the [Vercel AI Gateway speech-to-text endpoint](https://vercel.com/docs/ai-gateway/modalities/speech-to-text), using only the standard library. `transcribe.New(cfg.Transcription)` returns a `transcribe.Client`, whose one method, `Transcribe(ctx, audioPath)`, takes a file path and returns a `transcribe.Transcript`: the full `Text`, the audio `Duration`, and ordered `Segments`, each with `Start`, `End`, `Text` and an optional `Speaker`. A file path, not a reader, is the input so a multi-hundred-MB recording is never held in memory.

The gateway call is `POST {origin}/v4/ai/transcription-model` with `Authorization: Bearer <AI_GATEWAY_API_KEY>`, `ai-gateway-protocol-version: 0.0.1`, `ai-transcription-model-specification-version: 4`, `ai-model-id: <model>`, and a JSON body `{"audio": "<base64>", "mediaType": "audio/mpeg"}`. Multipart is not supported. The base64 is produced as the request streams from disk, so only one chunk is ever read, and never more than a few KiB is buffered. The request carries an exact `Content-Length`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `TRANSCRIPTION_PROVIDER` | `gateway` | `gateway` or `groq`. Anything else fails at startup. |
| `AI_GATEWAY_API_KEY` | none | Shared with the LLM client. A secret: set it in Railway, never in `.railway/railway.ts`. |
| `AI_GATEWAY_BASE_URL` | `https://ai-gateway.vercel.sh/v1` | Shared with the LLM client. The transcription endpoint lives under `/v4/ai/`, not `/v1`, so only the scheme and host of this URL are used (`https://ai-gateway.vercel.sh`). |
| `TRANSCRIPTION_MODEL` | `elevenlabs/scribe-v2` | Gateway model ID (`whisper-large-v3-turbo` when the provider is `groq`). `elevenlabs/scribe-v2` labels speakers and times every word by default. |
| `TRANSCRIPTION_TIMEOUT` | `5m` | Limit on one request, upload included, as a Go duration. Must be positive. |
| `TRANSCRIPTION_DIRECT_UPLOAD_MB` | `2` | Largest MP3 sent as is, in MiB. Anything larger, or not an `.mp3`, is re-encoded in chunks. |
| `TRANSCRIPTION_MAX_CHUNK_MB` | `8` | Largest single chunk, in MiB. The gateway documents no request size limit, so chunks stay small. |
| `TRANSCRIPTION_PROVIDER_OPTIONS` | none | A JSON object sent verbatim as the request's `providerOptions`, for example `{"azure":{"timestamps":"word","diarization":{"enabled":true}}}` for `microsoft/mai-transcribe-2`. A value that is not a JSON object fails at startup. |
| `GROQ_API_KEY`, `GROQ_BASE_URL`, `TRANSCRIPTION_MAX_UPLOAD_MB` | none, `https://api.groq.com/openai/v1`, `25` | Used only when the provider is `groq`, which keeps the previous multipart client. `TRANSCRIPTION_MAX_UPLOAD_MB` is both its direct-upload threshold and its chunk limit. |

Without a key for the selected provider the binaries start normally, and every `Transcribe` call returns `transcribe.ErrNotConfigured` without contacting the service.

The REST response is `{text, segments, language, durationInSeconds, warnings, providerMetadata}`, where each segment is `{text, startSecond, endSecond}`. Segments carry no speaker, so speaker data is read from `providerMetadata`:

- `elevenlabs.words` (`text`, `type` of `word`, `spacing` or `audio_event`, `start`, `end`, `speaker_id`). When `segments` is empty, segments are built from the words: a new segment starts on a speaker change, a pause of 1.5 s or more, after a word ending in `.`, `?` or `!`, or when a segment would pass 30 s. When `segments` is present, each takes the speaker of the word at its midpoint.
- `azure.phrases` (`text`, `offsetMilliseconds`, `durationMilliseconds`, `speaker`). Phrases become the segments, since they carry the speaker.

Models that return neither get segments from `segments`, or none (the `Text` is still set), and no speakers. The request and response shapes follow the docs and the `@ai-sdk/gateway` source; the committed tests run only against `httptest`.

### Speakers

`Segment.Speaker` is empty when the model gives no speaker. Otherwise it is `c<chunk>/<model id>`, such as `c0/speaker_0`. Models assign speaker IDs independently for every upload, so `speaker_0` in one chunk is not necessarily the same person as `speaker_0` in the next. The chunk prefix makes the IDs unique across the whole transcript and never claims that two chunks share a speaker; identifying the same person across chunks is left to a later step (for example the LLM pass, using the text). A file sent in one request is chunk 0.

### Chunking

The gateway path always re-encodes through `ffprobe` and `ffmpeg` (which must be on `PATH`; the worker image does not have them yet, CIV-34 adds ffmpeg), unless the file is an `.mp3` at or under `TRANSCRIPTION_DIRECT_UPLOAD_MB`. Chunks are 10 minutes long and overlap by 5 seconds, so each starts 9 minutes 55 seconds after the previous one. Each chunk is re-encoded as 16 kHz mono 32 kbps MP3 (about 2.4 MB, about 3.2 MB as base64). A chunk over `TRANSCRIPTION_MAX_CHUNK_MB` fails with `transcribe.ErrChunkTooLarge`. The `groq` provider uploads a file at or under `TRANSCRIPTION_MAX_UPLOAD_MB` as is and chunks larger ones.

Overlap means a sentence cut by a boundary is heard whole by one of the two chunks. Each chunk transcribes in order and its segment times are shifted by the chunk's offset. To keep overlapped speech from appearing twice, the boundary between two chunks is the middle of their overlap, and a segment is kept only by the chunk whose side of the boundary holds the segment's midpoint. The stitched `Text` joins the kept segments, and `Duration` is the `ffprobe` duration of the whole file. Chunks are extracted and uploaded one at a time, and the temporary directory holding them is removed on every return path, including errors and a canceled context.

### Errors

`Transcribe` does not retry; River retries the job, but a retry restarts the whole file. `transcribe.IsRetryable(err)` is true for network failures, the `TRANSCRIPTION_TIMEOUT` limit, a canceled context, 408, 429 (with `RetryAfter` from the `Retry-After` header when present), 5xx, 402 on the gateway (as in `internal/llm`; the balance can be topped up), and a 200 response that cannot be decoded. These are `*transcribe.Error` values. Everything else is permanent and a worker should wrap it in `river.JobCancel`: other 4xx statuses (including 413), `ErrNotConfigured`, `ErrInvalidProviderOptions`, `ErrChunkTooLarge`, `ErrResponseTooLarge` (a reply over 32 MiB), a missing audio file, and `ffmpeg` or `ffprobe` failures such as unreadable audio or a binary that is not installed.

### Tests

Tests never call the gateway or Groq; the client's own tests run against `httptest` servers and a fake `Splitter`. One test runs real `ffmpeg` on a generated tone and skips when `ffmpeg` or `ffprobe` is not installed. `internal/transcribe/transcribetest` provides `transcribetest.Fake`, a `transcribe.Client` for other packages' tests (`Returning(transcript)`, `Failing(err)`, or a `Respond` function; `Calls()` returns the paths received), and `transcribetest.FakeSplitter`.

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

## Backward compatibility check

Railway runs migrations before the new code starts, so for a short window the old code runs against the new schema. Squawk catches risky statements; the `migrations-compat` CI job proves the result. It applies the pull request's migrations (goose and River, as `make migrate` does) to a fresh Postgres 17, checks out the pull request's base commit into `base/`, and runs that code's `go test -tags=integration ./...` against the migrated database. If the old tests fail, the migration is not backward compatible and the job says so.

- The old tests call `testdb.Pool`, which runs the old code's own `migrations.Up`. Against the new schema that is a no-op: goose has nothing to apply when every source migration is already recorded, and it ignores recorded versions it has no file for. The database keeps the new schema while the old tests run.
- The check is skipped when every added or modified migration file contains a `-- migration: contract` line, since destructive changes are expected to break old code. A pull request that mixes contract and ordinary files is still checked.
- Run it locally against an empty database: `DATABASE_URL=... BASE_REF=origin/main .github/scripts/check-migration-compat.sh [api/migrations/NNNNN_name.sql ...]`. Set `BASE_DIR` instead of `BASE_REF` to use an existing checkout.
- Limits: the base commit is the pull request's base when the event fired, which approximates production (`main` after the latest deploy). If the base tests need something the pull request removes, they fail, and that is the signal. Editing an already applied migration file is not modelled.

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
