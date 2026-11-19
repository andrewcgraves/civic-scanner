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

Every route is under `/v1`. All are public except `GET /v1/me`, which needs a WorkOS access token (see [Authentication](#authentication)). `openapi/v1.yaml` is the full contract. Errors are JSON `{"code", "message"}` bodies.

| Route | Returns |
| --- | --- |
| `GET /v1/healthz` | `ok`, or `degraded` with 503 when the database is unreachable |
| `GET /v1/agencies` | every live agency |
| `GET /v1/agencies/{slug}` | one live agency, the same shape as a list item |
| `GET /v1/agencies/{slug}/meetings` | one page of the agency's meetings, newest first |
| `GET /v1/meetings/{id}` | one meeting of a live agency, with its agency, latest agenda version and recordings with their transcripts |
| `GET`, `HEAD /v1/agencies/{slug}/feed.xml` | an RSS 2.0 feed of a live agency's latest meeting events, behind a flag (see "Agency RSS feeds"); the website serves it at `/agencies/{slug}/feed.xml` |
| `GET /v1/me` | the signed-in user (`id`, `email`, `createdAt`), creating it on first sight; behind the `accounts` flag |

- **Not live is not found:** an unknown slug and a pending or rejected agency get the same 404 (`not_found`), so a caller cannot tell whether an unpublished agency exists. A live agency with no meetings returns an empty list.
- **Meeting items** carry `id`, `title`, `startsAt` (UTC), `status` (`scheduled`, `held`, `cancelled`) and `sourceUrl`, the agency's page for the meeting, left out when the source gave none. New optional fields, such as agenda or summary links, can be added later without breaking clients, which must ignore fields they do not know.
- **Pagination:** keyset, not offset. Meetings are ordered by `starts_at DESC, id DESC`; the id breaks ties between meetings that start at the same time. A page asks for `limit` (default 50, at most 200) and returns `nextCursor`, which is `null` on the last page. Passing it back as `cursor` returns the meetings strictly after the last one on the previous page, so a meeting added or changed between requests never makes another one skipped or repeated. `meetings_agency_starts_at_idx` (`agency_id, starts_at DESC, id DESC`) serves the query.
- **Cursors** are opaque to clients. Inside, a cursor is base64url (no padding) of a small JSON object holding the agency id and the last meeting's start time and id, followed by the first 8 bytes of its SHA-256. The checksum catches cursors that were truncated, edited or corrupted; it is not a secret, because a cursor only names a position in public data. A cursor that does not decode, fails its checksum, or belongs to another agency is a 400 (`invalid_cursor`), never a restart from the first page. A `limit` outside 1 to 200 or not an integer, and any other parameter the router cannot parse, is a 400 (`invalid_parameter`).
- **Order of checks:** a malformed `limit` is rejected before the agency is looked up. The cursor is checked after, since it is tied to the agency, so an unknown agency with a bad cursor is a 404.
- **Meeting detail:** `GET /v1/meetings/{id}` returns the list item's fields plus:
  - `agency`, the same shape as `GET /v1/agencies/{slug}`, so the meeting page needs one request.
  - `agenda`: the latest version (`version`, `documentUrl`, `summary`, `changeNote`, `foundAt`), or `null` before an agenda is published. `foundAt` is when the service first found the version (`created_at`), not the agency's publication date. `summary` and `changeNote` are `null` until written; the first version never has a change note. `documentUrl` is the agency's original document. The extracted agenda text is not returned: the meeting page shows the summary and links to the agency's document, and the text can run to hundreds of kilobytes. Older versions are not returned either.
  - `recordings`: every recording in the order found (`seq`), empty before there is one. Each has `sourceUrl`, the agency's original recording, and `transcript`, `null` until the recording is transcribed. A transcript has `summary` (`null` until written) and `segments` (`startMs`, `endMs`, `text`, and `speaker` when known). There is no `updatedAt`: the row's `updated_at` also changes when the summary is written, so it cannot say when the text changed. The segments are the full timestamped transcript the meeting page shows, so they are returned inline; the full text is not, since it repeats the segments. A meeting split into several videos has several recordings, each with its own transcript.
  - Nothing about stored audio (key, expiry, deletion) is exposed: the recording link is always the agency's, whether or not we still hold the audio.
  - Transcript segments are returned inline and uncompressed (a three-hour meeting is roughly 400 KB), and stored source URLs are returned as stored, so a consumer rendering them as links must accept only `http` and `https`.
  - The 404 rule is the agencies': an unknown id and a meeting of an agency that is not live get the same `not_found`. An id that is not a UUID is a 400 (`invalid_parameter`), like any other parameter the router cannot parse.
  - `store.GetLiveMeetingDetail` reads it in three queries (meeting joined with its live agency, latest agenda version, recordings left-joined with transcripts) in one read-only `REPEATABLE READ` transaction, so the parts are consistent with each other and the query count does not grow with the number of recordings. The agenda and transcript it returns leave `Text` empty. The unique constraints on `(meeting_id, version)`, `(meeting_id, source_url)` and `recording_id` serve the queries.
- **Caching:** the JSON routes set no `Cache-Control` or validators, the same as `GET /v1/agencies`. A caching policy for the public JSON reads is a separate decision. The RSS feed has its own, below.

### Agency RSS feeds

`GET /v1/agencies/{slug}/feed.xml` is an RSS 2.0 feed of one live agency's meeting events, read from the outbox (see "Meeting events"). `HEAD` returns the same status and headers without the body. The website serves the same feed at `/agencies/{slug}/feed.xml` (see "Feed on the website" below), which is the address subscribers use.

- **Flag and configuration.** The route answers only when the `agency-rss-feeds` flag is on and `PUBLIC_WEB_URL` is set. Otherwise it returns the same 404 as an unknown agency, before touching the cache or the database. The JSON reads have no flag; the feed has one because the story asks for it and because subscribers keep a feed URL for good.
- **The flag is evaluated anonymously.** The `authenticate` middleware puts a signed-in user's id in the flag targeting context of every request. The feed handler drops it (`featureflags.Anonymous`) before checking `agency-rss-feeds`, so the answer is the same with or without a token. The 200 is `Cache-Control: public`, and a per-user answer could let a shared cache hand a staff-only feed to everyone. Nothing in the response depends on request headers other than `If-None-Match`, so there is no `Vary`.
- **Keep the flag off until the pages exist.** Item links point at `{PUBLIC_WEB_URL}/meetings/{id}` and the channel link at `{PUBLIC_WEB_URL}/agencies/{slug}`. Those pages do not exist yet: CIV-43 adds the meeting page and CIV-36 the agency page. Until both have shipped every link in the feed is a 404, so `agency-rss-feeds` should stay off. Autodiscovery (a `<link rel="alternate" type="application/rss+xml">` on the agency page) is not included for the same reason: there is no agency page to put it on.
- **`PUBLIC_WEB_URL`.** The origin of the public website, such as `https://civicscanner.org`, optionally with a path prefix. The API reads it at startup and refuses to start when it is not an absolute `http` or `https` URL with a host, a port (if any) from 1 to 65535, and no user info, query or fragment. A trailing `/` is dropped. The Atom `rel="self"` link is `{PUBLIC_WEB_URL}/agencies/{slug}/feed.xml`, the website's address for the feed, so subscriptions do not depend on the API version or host.
- **Items.** One per event, newest first by `sequence`, from the agency's 50 newest events (`store.MaxFeedItems`). Each event type has its own case: `agenda_published` is `Agenda posted: `, `agenda_changed` is `Agenda updated: `, `transcript_ready` is `Transcript ready: `, each followed by the meeting title and its date. An event of any other type is left out. The `guid` is the event's uuid with `isPermaLink="false"`, so it never changes and never repeats. `pubDate` is the event's `occurred_at` in RFC 1123 form with a numeric zone, in UTC; the channel's `lastBuildDate` is the newest item's, so an unchanged feed renders byte for byte the same. Descriptions say which agency did what for which meeting and when, then the change note (`agenda_changed`) and the summary of the version the event names (both agenda types). Meeting times are shown in `America/Los_Angeles`, since every tracked agency is in Southern California; an agency elsewhere needs a time zone on the agency. A transcript item says only that the transcript is ready: the event payload is not read, so nothing a job puts in it is exposed. Nothing else leaves the database: no agency, version or recording ids, storage keys, document URLs or agenda text.
- **Agenda items before their summary.** An agenda event is published as soon as it is in the outbox. Its summary is written by a separate job afterwards, so readers who poll first see `A summary is not available yet.`; the description fills in once the summary exists and the cached feed expires, but most readers do not show an item again after they first fetch it. Holding an agenda item back until it has a summary or a grace period has passed is the constant `agendaSummaryGrace` in `internal/httpapi/rss.go` (currently 0, no hold); the renderer already applies it, and `TestRendererCanHoldAgendaItemsUntilTheirSummary` covers it. With a hold, the 50-event window shows fewer items while summaries are pending.
- **Untrusted text.** Agency names and meeting titles come from agency websites; summaries and change notes from a model. The feed is built with `encoding/xml`, which escapes every text node and attribute; there is no CDATA. Before that, each string loses invalid UTF-8 (which includes encoded surrogates), control characters other than tab and line breaks (`\r\n` and `\r` become line breaks), U+FFFE and U+FFFF, every Unicode format character (category Cf: bidi controls, zero-width characters, byte order marks, soft hyphens) and the tag block U+E0000 to U+E007F, the same set the summary cleaning removes. Titles collapse to one line and are cut at 300 bytes; change notes and summaries keep their non-blank lines and are cut at 4,000 bytes, on a UTF-8 boundary, ending with `…`. Titles and the channel description are plain text. RSS readers render an item `description` as HTML, so each line of description text is HTML-escaped and wrapped in `<p>`, then XML-escaped: a summary containing `<script>` reaches the reader as the text `<script>`, never as markup. The only markup in a description is those `<p>` elements.
- **Size cap.** A rendered feed is at most 1 MiB (`maxFeedBytes`). Escaping can grow description text about ninefold (`&` becomes `&amp;amp;`), so 50 hostile items could otherwise reach several megabytes. The renderer measures each item and keeps the newest items that fit. Ordinary feeds are a few kilobytes and never reach the cap.
- **Polling and missed items.** The feed is a fresh read of the newest events, not a cursor, so an event whose transaction commits after events with higher sequence numbers appears as soon as it commits (and the cached feed expires), at its sequence position. Feed readers deduplicate by `guid`, not by position, so they pick it up on their next poll. A reader only ever sees the newest 50: if more than 50 events of one agency commit between two of its polls, the older ones of that burst have left the feed before it looks, and that subscriber never sees them. At a handful of events per agency per day that takes a reader polling less than about weekly, or a burst such as a re-ingest of many meetings at once.
- **HTTP.** `Content-Type: application/rss+xml; charset=utf-8`. `ETag` is a strong validator, the first 16 bytes of the SHA-256 of the body in hex, so it changes whenever anything in the feed does, including a summary written after its event. `If-None-Match` with a matching tag (weak comparison, a list of tags, or `*`) gets a 304 with the `ETag` and `Cache-Control` and no body. Several `If-None-Match` header lines are joined into one comma-separated list by a router middleware before the generated parameter binding, so they behave like one line. There is no `Last-Modified`: no single timestamp covers every change, and a wrong one would make readers miss updates. `Cache-Control: public, max-age=300`. Any method other than `GET` or `HEAD` is a 405 with `Allow: GET, HEAD`; the 405 handler asks the router which methods the path has, so the other paths still say `Allow: GET`.
- **Server-side cache.** The endpoint is public and unauthenticated, and the query's cost grows with the agency's meetings, so the rendered feed (body and ETag) is cached in process per slug for 60 seconds (`feedCacheTTL`, under the 300-second `max-age`), for at most 256 slugs; when full, expired entries are dropped first, then the entry closest to expiry. Concurrent requests for a slug whose entry is missing or expired share one load (single flight): the first starts the query, detached from its own request's cancellation and limited to 10 seconds, and the others wait for it, each giving up when its own request ends. Conditional requests and `HEAD` are answered from the cached entry, so a reader polling with `If-None-Match` costs no query while the entry is fresh. Only successful loads are cached. An unknown or not-live slug and an error are not, so made-up slugs cannot fill the cache or pin a 404, and each costs one cheap query (the agency lookup finds no row and nothing else runs). The flag is checked on every request before the cache, so turning it off takes effect at once. Each API replica has its own cache; a change can take up to 60 seconds to show on the server, plus the client's `max-age`.
- **The query.** `store.GetLiveAgencyFeed` is one statement: the live agency by slug, left-joined laterally to its meetings, each laterally to its newest events through `meeting_events_meeting_sequence_idx`, the top 50 by `sequence` across them, and each agenda event's version from `agenda_versions` through an index (the planner picks `agenda_versions_pkey` with the meeting as a filter or `agenda_versions_meeting_version_unique` with the id as a filter, depending on statistics). The version must belong to the event's own meeting, and the payload's `agenda_version_id` is used only when it is a well-formed uuid, so a malformed payload, or one naming another meeting's version, leaves the item without a summary instead of failing the feed or showing the other meeting's text. No row means no live agency; one row with null event columns is an empty feed. The agency's meetings come from a bitmap scan of `meetings_agency_source_unique`, and every one of them is probed for events, so the cost grows with the agency's number of meetings, not with other agencies'. `EXPLAIN (ANALYZE, BUFFERS)` on a seeded database (40 agencies with 1,000 meetings each and one with 50,000): an agency with 1,000 meetings and 2,000 events took about 5 ms and 5,700 buffer hits; the agency with 50,000 meetings and one event about 50 ms and 151,000 buffer hits. On a cache miss a 304 costs the same query as a 200. Joining events to meetings first and scanning `sequence` backwards, the obvious form, reads every event in the table for an agency with few recent events. An `agency_id` column on `meeting_events` with an index on `(agency_id, sequence)` would make the read constant-cost; that needs a migration and a backfill and is left for when an agency's history makes it worth it.
- **Contract.** `openapi/v1.yaml` declares the `200` as `application/rss+xml; charset=utf-8` with a string body, the `ETag` and `Cache-Control` headers, `If-None-Match` as a header parameter, and a `304`. oapi-codegen turns that into `GetAgencyFeed200ApplicationrssXmlCharsetUtf8Response` (an `io.Reader` body, `ContentLength` and typed headers) and `GetAgencyFeed304Response`, and `head` into a `HeadAgencyFeed` operation, so the strict handler writes the content type and headers and the spec stays the source of truth.

#### Feed on the website

`web/src/app/agencies/[slug]/feed.xml/route.ts` serves `/agencies/{slug}/feed.xml`, the address in the feed's self link, by fetching the API's `/v1/agencies/{slug}/feed.xml` on the server (from `NEXT_PUBLIC_API_BASE_URL`, like every other API call).

- `GET` and `HEAD` pass through the API's status and body and only its `Content-Type`, `ETag` and `Cache-Control`. A 304 passes through without a body.
- The only request header sent upstream is `If-None-Match`. Cookies, `Authorization` and forwarding headers are not.
- The slug must be lowercase letters and digits in hyphen-separated groups, at most 100 characters, before an upstream URL is built. Anything else is a 404 without a request to the API.
- Redirects are not followed (`redirect: "manual"`): a redirect from the API is a 502, so the website never fetches from another host. An unreachable API, or one slower than 10 seconds, is a 502 with `Cache-Control: no-store`.
- The route is dynamic and the upstream fetch is `no-store`, so Next.js keeps no copy of its own. Caching follows the API's `Cache-Control`, which Vercel's CDN and readers honour.

## Authentication

The web app signs users in with WorkOS AuthKit (see [Sign-in on the web](#sign-in-on-the-web)) and sends the AuthKit access token to the API as `Authorization: Bearer <token>`, from its server only. The API verifies the token itself, against the WorkOS key set, and never calls WorkOS per request. `internal/auth` does the verification with the standard library only.

| Variable | Default | Meaning |
| --- | --- | --- |
| `WORKOS_CLIENT_ID` | none | The WorkOS client id (`client_...`) of the environment. Unset means not configured: the API starts, public routes work, and signed-in routes answer 503 `auth_not_configured`. Not a secret. |
| `WORKOS_ISSUER` | `https://api.workos.com/user_management/<client id>` | Comma-separated list of accepted `iss` values, matched exactly. WorkOS environments created before mid-2025 issue `https://api.workos.com`, and a custom auth domain issues its own; copy the value from a decoded token. |
| `WORKOS_AUDIENCE` | none | When set, the token's `aud` must contain it. AuthKit session tokens carry no `aud` unless a JWT template adds one; add `"aud": "<API URL>"` to the template, then set this. |
| `WORKOS_BASE_URL` | `https://api.workos.com` | Origin the key set is read from (`<base>/sso/jwks/<client id>`). Must be a bare `https` origin, or the binaries stop at startup. |

A token is accepted only when all of these hold (sources: the WorkOS [session tokens reference](https://workos.com/docs/reference/authkit/session-tokens), and the guide [How to verify WorkOS access tokens in your own API](https://workos.com/blog/verify-workos-access-tokens-in-your-own-api)):

- It is a compact JWS of at most 8 KiB, with strict unpadded base64url, header `alg` exactly `RS256` (so `none`, `HS256` signed with the public key, `RS512` and `PS256` are refused before any key is looked up), a `kid`, and no `crit` member at all (`"crit": null` is refused too).
- The RS256 signature verifies against the key with that `kid` in the WorkOS key set.
- `iss` is one of `WORKOS_ISSUER`, `client_id` equals `WORKOS_CLIENT_ID`, and `aud` contains `WORKOS_AUDIENCE` when that is set. The `client_id` check binds the token to this environment even when the issuer is the shared `https://api.workos.com`.
- `exp` and `iat` are present and numeric; `exp` is in the future, and `iat` and `nbf` (when present) are not, each with 10 seconds of leeway for clock skew.
- It is a user access token: `sub` and `sid` are non-empty strings, `auth_time` is present and numeric, `sub_profile` is either absent or exactly the string `"user"`, and `sub_type` is absent. Presence is what counts: `"sub_profile": null` and `"sub_type": null` are refused. Agent and widget tokens signed by the same environment carry those claims and are refused. These are the claim checks of `hasRequiredAccessTokenClaims` in `@workos-inc/node` 12.3.0 (string `sub`, `sid` and `iss`, numeric `exp`, `iat` and `auth_time`, `sub_profile` absent or `user`, no `sub_type`), plus non-empty `sub` and `sid`. Like the SDK, the API cannot tell a Connect consent token, which also carries `sid` and `auth_time`, from a session token offline (WorkOS's own API does it by resolving `sid` to a live session); the `client_id` check refuses consent tokens issued to other applications.
- An `act` claim, present when a WorkOS admin impersonates the user, must be an object; its `sub` becomes `Identity.Actor`. The user is still `sub`, and no user is created for the actor. Nothing reads `Actor` yet; write endpoints can use it to refuse or audit impersonated requests.

The key set:

- Keys are kept only when they are RSA, have a `kid`, are for signing (`use` absent or `sig`, `alg` absent or `RS256`), and have a 2048 to 8192-bit modulus and an odd exponent. A duplicate `kid` keeps the first.
- It is fetched on first use with a 5 second timeout, over `https` only, without following redirects, and refused when the body is over 1 MiB or not 200.
- It is fresh for an hour. After that a token whose `kid` is known is still accepted while the set is refreshed in the background.
- A `kid` that is not in the set triggers one refresh, which the request waits for. Concurrent requests share one fetch. Refreshes start at most once every 10 seconds, so a flood of tokens with made-up `kid`s costs WorkOS at most six requests a minute.
- An unknown `kid` is a 401 only when the request waited for a refresh that succeeded, so the set is known to be current. Within the 10 seconds after a refresh, an unknown `kid` is answered at once with 503 `auth_unavailable`: the token may be signed with a key WorkOS rotated in since, and the client should retry rather than sign the user out.
- A failed refresh is logged once at `warn`; the requests it affects are logged at `debug` only, so a flood during a WorkOS outage cannot fill the logs.
- When a refresh fails, the last good set is used for up to 24 hours after it was fetched (stale-if-error), so a WorkOS outage does not sign everyone out. Past that, or with no set at all, tokens cannot be checked and signed-in routes answer 503 `auth_unavailable`. - Key revocation: a key WorkOS removes from the set is still trusted until the next refresh, within about an hour (the freshness window), or for up to 24 hours while WorkOS's key set is unreachable. Tokens still expire on their own `exp`.

Requests:

- `internal/httpapi` runs the `authenticate` middleware on every route. Without an `Authorization` header it does nothing but record that there is no token. With one, it verifies the token and stores the result, an `auth.Identity` (WorkOS user id, session id, organization, role, permissions, email, expiry) or the error, in the request context. It never rejects a request itself, so public routes answer the same whether the token is valid, expired, garbage or not checkable.
- A valid token also sets the WorkOS user id as the OpenFeature targeting key for the request (`featureflags.WithIdentity`), so flags checked later in the request can be rolled out per user. With PostHog as the provider, that WorkOS user id is sent to PostHog as the distinct id.
- Signed-in handlers call `h.requireUser(ctx)`, which returns the `store.User`, creating it on first sight, or an error to map: `ErrNoCredentials` and `auth.ErrInvalidToken` to 401, `auth.ErrNotConfigured` and `auth.ErrUnavailable` to 503. `httpapi.IdentityFrom(ctx)` returns the verified identity without touching the database.
- 401 is always `{"code":"unauthorized","message":"a valid bearer token is required"}` with `WWW-Authenticate: Bearer`, whatever the reason; the reason is logged at `debug`. 503 means the token could not be checked, not that it is bad, so a client must not sign the user out on it. Tokens are never logged.
- `GET /v1/me` checks the token before the flag: no token or an invalid one is a 401, and a token that cannot be checked a 503, whether the flag is on or off, so a client can always tell "refresh the token" from "feature off". Only a verified user is checked against the `accounts` flag, evaluated for that user; with the flag off for them it answers 404 `not_found` and creates no user.
- Every response of a signed-in route (`GET /v1/me`, all statuses) carries `Cache-Control: no-store`. The `noStore` strict middleware sets it for the operations listed in `privateOperations`; add new signed-in operations there.
- The API has no CORS handling, so a browser on another origin cannot call it with a bearer token. The web app makes signed-in calls from its server (`callApiAsUser`, below); a browser never holds the token.

Users (`users`, migration `00009`):

- One row per WorkOS user, unique on `workos_user_id` (the token's `sub`), with our own `id` (uuid), a nullable `email`, `created_at` and `updated_at`. The WorkOS id is the only identity key; `GET /v1/me` returns our `id`, not the WorkOS id.
- AuthKit access tokens carry no email. The API reads an optional `email` claim, which the environment's [JWT template](https://workos.com/docs/authkit/jwt-templates) adds with `{"email": {{ user.email }}}`. Without the template `email` stays null; the API does not call the WorkOS user API, so it needs no WorkOS secret. Email is mutable data: it is not unique, a token with a different email updates it, and a token without one leaves it alone.
- `store.EnsureUser` reads the row first and writes only when the user is new or the email changed. The write is `INSERT ... ON CONFLICT (workos_user_id) DO UPDATE`, so concurrent first requests for one user create exactly one row and all get it back.
- Role (CIV-25) and plan (CIV-37) are later columns on `users` and later optional fields on `GET /v1/me`.

`.railway/railway.ts` sets no `WORKOS_*` variables yet, so in production signed-in routes answer 503 `auth_not_configured` until the client id is added there.

Not verified against a real WorkOS environment: the tests sign tokens with generated RSA keys and serve the key set from `httptest`.

### Sign-in on the web

The web app uses [`@workos-inc/authkit-nextjs`](https://github.com/workos/authkit-nextjs) with the hosted AuthKit page, so the sign-in methods (Google, Apple, GitHub, magic link) are whatever the WorkOS environment enables; the app has no code per method. Sign-in is on only when WorkOS is configured **and** the `accounts` flag is on (see [Feature flags](#web)). `web/src/lib/auth/` holds it.

| Variable | Meaning |
| --- | --- |
| `WORKOS_CLIENT_ID` | The client id (`client_...`) of the WorkOS environment, the same value the API uses. |
| `WORKOS_COOKIE_PASSWORD` | At least 32 characters; encrypts the session cookie. A secret. Generate it with `openssl rand -base64 32`, one per environment. There is one password and no rotation: changing it (for example after a leak) makes every existing cookie unreadable, which signs everyone out and abandons sign-ins in progress. |
| `NEXT_PUBLIC_WORKOS_REDIRECT_URI` | The callback URL, `https://<site>/auth/callback` (`http` only on `localhost`). It must be listed as a redirect URI in WorkOS. Inlined at `next build`, like every `NEXT_PUBLIC_` value. |

`WORKOS_API_KEY` is not used: AuthKit runs as a PKCE public client, so the web app holds no WorkOS secret besides the cookie password. Leave it unset everywhere the web app runs, because the WorkOS SDK reads it from the environment by itself. AuthKit's optional variables (`WORKOS_COOKIE_NAME`, `WORKOS_COOKIE_DOMAIN`, `WORKOS_COOKIE_MAX_AGE`, `WORKOS_COOKIE_SAMESITE`, `WORKOS_ISSUER`, `WORKOS_API_HOSTNAME`) keep their defaults and should stay unset unless there is a reason; `WORKOS_COOKIE_SAMESITE=none` must never be set.

Configuration states (`web/src/lib/auth/config.ts`):

- **Off:** none of the three is set. Nothing about sign-in runs: no header, the `/auth/*` routes answer 404, the proxy returns at once, nothing is logged. This is local development, tests, preview deploys and production until CIV-3 provisions WorkOS.
- **Misconfigured:** some are set but one is missing, the client id does not start with `client_`, the password is shorter than 32 characters, or the redirect URI is not an `https` (or `localhost`) URL whose path is exactly `/auth/callback` with no user info, query or fragment. Sign-in stays off exactly as above, and the server logs `Sign-in is disabled because WorkOS is misconfigured: <reasons>` once per process at startup (`src/instrumentation.ts`). The reasons name variables, never values. The site keeps working rather than failing to start, because a mistake in an optional, flagged feature must not take the public pages down.
- **On:** all three valid. The header and routes still wait for the `accounts` flag.

The header is decided from non-secret values only: the root layout reserves it when `NEXT_PUBLIC_WORKOS_REDIRECT_URI` is a valid callback URL (`hasPublicRedirectUri`) and the `accounts` flag is on. Both are inlined at `next build` in in-memory flag mode (and evaluated per request in PostHog mode), and neither needs the secrets, so a static page built where `WORKOS_COOKIE_PASSWORD` or `WORKOS_CLIENT_ID` is absent still offers sign-in once they are present at run time. Whether sign-in actually works is the server's answer: `/auth/session` returns 404 unless the full configuration is valid at run time, and the header then stays empty.

Flag: `accounts`, the same key that gates `GET /v1/me` in the API, so one PostHog flag turns accounts on for both sides. The web evaluates it for its one anonymous identity (see the identity note under [Web](#web) flags) and the API per user, so a percentage rollout gives different answers; turn it on for everyone or no one until web flags are per user. If the API says 404 for a user the web considers signed in, the header shows the neutral label.

Routes (`web/src/app/auth/`):

| Route | What it does |
| --- | --- |
| `GET /auth/sign-in?returnTo=<path>` | Starts sign-in: `getSignInUrl`, always given the configured `NEXT_PUBLIC_WORKOS_REDIRECT_URI` (AuthKit would otherwise take an `x-redirect-uri` request header, which the proxy does not strip on this route), seals a PKCE verifier and nonce into a 10-minute `wos-auth-verifier-<hash>` cookie and the OAuth `state`, then a 307 to the AuthKit authorization URL. Building the URL is local; nothing is sent to WorkOS. |
| `GET /auth/callback` | AuthKit's `handleAuth`: requires the verifier cookie to match `state` (CSRF), exchanges the code with the PKCE verifier, sets the session cookie, deletes the verifier cookie, and redirects to the return path on the redirect URI's origin. Any failure is a plain 400 `Sign-in failed`; AuthKit logs the error with whether a code and state were present, never their values. |
| `GET /auth/session` | `{"signedIn": false}` or `{"signedIn": true, "email": <string or null>}`, `Cache-Control: private, no-store`. Calls `GET /v1/me` for the email. When the proxy kept a session it could not verify (the WorkOS key set or a refresh was unavailable), it answers signed in without an email rather than signed out. |

Sign-out is the server action `signOutAction` (`web/src/lib/auth/actions.ts`), a POST from a form, never a link. It deletes the session cookie and any verifier cookies and redirects to WorkOS's logout URL for the session with `returnTo` set to the site root, `<origin of NEXT_PUBLIC_WORKOS_REDIRECT_URI>/` (never taken from the request), which ends the WorkOS session and sends the browser home. That exact URL (for example `https://civicscanner.org/`) must be allowed as a sign-out redirect in the WorkOS dashboard, or WorkOS refuses the logout redirect. Next.js checks the action's `Origin` against the host, and the cookie is `SameSite=Lax`, so another site cannot sign a user out.

Return paths: `returnTo` goes through `safeReturnPath` (`return-path.ts`) before it is sealed into the state. The value is checked, normalised with the URL parser (dot segments resolved, the fragment dropped), and the normalised path and query are checked again, so `/.//host` and `/x/..//host`, which normalise to `//host`, are refused. A path is kept only if it starts with a single `/`, has no backslash, space or control character, is at most 2,048 characters, and, once percent-decoded, does not start with `//`, contain a backslash or a `.` or `..` segment, or lie under `/auth` in any letter case; anything else, including absolute and `javascript:` URLs, becomes `/`. The callback also builds the final URL on the redirect URI's origin from only the path and query, so a return path cannot leave the site either way.

The proxy (`web/src/proxy.ts`) runs only for `/auth/session` (`matcher: ["/auth/session"]`), and returns at once unless WorkOS is configured and the request has a session cookie. Then AuthKit's `authkit()` verifies the access token against the WorkOS key set (fetched once and cached by `jose`), refreshes it when it expires within 60 seconds, and passes the session to the route in request headers that never reach the browser; the configured redirect URI is passed explicitly, and client-sent `x-workos-*`, `x-redirect-uri` and `x-civic-session-state` headers are dropped. The proxy also tells the route, in `x-civic-session-state`, whether it refreshed the session in this request (`refreshed`) or kept a cookie whose session it could not verify (`unverified`). It never sets a sign-in verifier cookie on this route. Pages, the RSS route and static assets never go through it, so the route table is unchanged: `/` stays static and `/agencies` and the feed stay dynamic as before.

The header (`web/src/components/account-menu.tsx`) is rendered by the root layout only when the header is reserved as above. Without it the visible HTML is unchanged; the page payload still references the account menu's client chunk and carries a null child where the header would be. The header is one line high (`h-6`) in every state, so nothing on the page moves when the session arrives. It is a client component with no props: after hydration it fetches `/auth/session` and shows nothing until the answer arrives (and nothing if the request fails), so static pages stay static and nobody sees a flash of the wrong control. Signed out, it is a `Sign in` link (a plain `<a>`, so nothing prefetches it and starts a flow) with the current path and query as `returnTo`. Signed in, it is a button labelled with the email, or `Account` without one, that discloses a panel with a `Sign out` button (`aria-expanded` and `aria-controls`; opening it moves focus to `Sign out`; Escape closes it and returns focus to the button; focus leaving it or a pointer down outside closes it). It uses the page's underline link style; there is no mockup, and no account, dashboard or billing page. Cost: about 1.3 KB gzipped of first-load JavaScript on every page even with sign-in off, and with sign-in on one `/auth/session` request per full page load, which for a signed-in user also calls `GET /v1/me`.

Calling the API as the user (`web/src/lib/auth/api.ts`, server only):

```ts
const me = await callApiAsUser((api) => api.GET("/me"));
```

- `api` is the generated `openapi-fetch` client on `NEXT_PUBLIC_API_BASE_URL` with `Authorization: Bearer <access token>` from the session. Its fetch refuses any URL outside that origin and path, so the token cannot go to another host, and it uses `redirect: "manual"` (a redirect is a failure, never followed), `cache: "no-store"` and a 10-second timeout.
- The result is `ok` with the data, `signed-out` (no session; or a 401, a refresh with `refreshSession`, and a second 401; or a refresh token WorkOS rejects; or a 401 for a token the proxy refreshed in this same request, in which case it does not refresh again, since `refreshSession` reads the request's cookie and would replay the refresh token the proxy has just spent), `unavailable` (a 503 from the API, or a refresh that failed for a transient reason; the user stays signed in), `not-found` (a 404: accounts are off for this user) or `failed` (anything else, including an unreachable API). On `signed-out` after its own refresh, the session cookie is deleted; after a proxy refresh it is left alone, so the response carries one session `Set-Cookie`, the proxy's. A call that throws or ends in an unexpected status is logged once at `warn` with the error name or status only, never the token or URL.
- It needs the AuthKit proxy on the route (add the path to the matcher) and a context that can write cookies, a route handler or a server action, because a refresh rotates the refresh token and must store the new one. Do not call it while rendering a server component.

What the browser stores (privacy):

- `wos-session`: `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` whenever the redirect URI or the request is `https`, 400-day max age (the tokens inside expire much sooner). Its value is sealed with iron-session (AES-256-CBC with an HMAC-SHA256, keyed by `WORKOS_COOKIE_PASSWORD`) and holds the WorkOS access token, the refresh token, the WorkOS user object (WorkOS user id, email, whether it is verified, name, first and last name, profile picture URL, locale, last sign-in time, created and updated times, external id, metadata), the impersonator's email and reason when a WorkOS admin impersonates the user, and the authentication method. JavaScript cannot read it, and the tokens are never in page HTML, props, JSON responses, URLs, `localStorage` or logs.
- `wos-auth-verifier-<hash>`: the same attributes, 10 minutes, only during sign-in: a nonce, the PKCE code verifier, the return path and the redirect URI, sealed the same way.
- What the app reads about the user: the session's access token, to call the API, and the email the API returns from `GET /v1/me`. The header shows only that email. The app stores nothing else, and sends nothing about the user to analytics (see below).

Not verified: no WorkOS environment exists yet (CIV-3), so sign-in has not been run end to end against WorkOS. Most tests mock the SDK; `web/src/lib/auth/sdk.test.ts` runs the real SDK through the sign-in and callback routes against a local stub of the WorkOS API (`WORKOS_API_HOSTNAME`, `WORKOS_API_HTTPS`, `WORKOS_API_PORT`) that serves a key set and the code exchange, and checks the authorization URL, the ignored `x-redirect-uri`, the verifier and session cookie attributes, the sealed return path round trip with hostile values, and that a mismatched or missing verifier is refused without contacting WorkOS. `vitest.config.ts` inlines the SDK so its `next/headers` import can be mocked.

## Feature flags

Unfinished work ships dark behind a flag. Both sides evaluate flags through [OpenFeature](https://openfeature.dev), with PostHog as the provider when a key is set and an in-memory list otherwise. Each side has its own variables.

### Web

The web app evaluates every flag on the server, through `@openfeature/server-sdk`, and hands the results to client components. `web/src/lib/flags/` wraps it. Callers import only from there, never from OpenFeature or PostHog.

- **Registry:** every flag is declared in `web/src/lib/flags/registry.ts`. `FLAG_KEYS` lists the keys and `FlagKey` is their union type, so checking a flag that is not registered is a type error. At run time an unregistered flag is off. Add the key there before using it. `accounts` turns on sign-in (see [Sign-in on the web](#sign-in-on-the-web)). `test-only-flag` exists only for the module's own tests.
- **Server components:** `await isFlagEnabled("flag-key")` from `@/lib/flags/server`.
- **Client components:** `useFlag("flag-key")` from `@/lib/flags/client`. The root layout calls `getFlagsOn()` from `@/lib/flags/server`, which evaluates each registered key with the OpenFeature client, and passes the keys that are on to `FlagsProvider`, a plain React context that supplies `useFlag`. A client component's server-rendered HTML and its first render in the browser therefore already show the right value: a flag that is on does not flash off, and hydration does not mismatch.

Every flag is a boolean and defaults to off. An unregistered or unknown flag, a multivariate PostHog flag, a PostHog error, a slow or unreachable PostHog, a provider that fails to start, and a client component outside `FlagsProvider` all evaluate to off.

The provider depends on the environment:

| Variable | Meaning |
| --- | --- |
| `NEXT_PUBLIC_POSTHOG_KEY` | PostHog project API key. When set, PostHog serves flags and `NEXT_PUBLIC_FEATURE_FLAGS` is ignored, and the browser sends [analytics](#analytics). |
| `NEXT_PUBLIC_POSTHOG_HOST` | PostHog API host. Defaults to `https://us.i.posthog.com`. |
| `NEXT_PUBLIC_FEATURE_FLAGS` | Used when there is no PostHog key: a comma-separated list of flags that are on, or `*` for every registered flag. Unset or empty means all off. For local development, tests, and preview deploys. |

Notes:

- **Swapping vendors:** `createProvider` in `web/src/lib/flags/server.ts` is the only place a provider is chosen, and it is registered with OpenFeature once per process. Moving to another vendor, such as LaunchDarkly, means returning that vendor's OpenFeature server provider from `createProvider`. Callers, the registry and `client.tsx` do not change.
- `NEXT_PUBLIC_` values are inlined at `next build`, so changing one needs a new build. On Vercel, set them per environment: the PostHog key for Production, and `NEXT_PUBLIC_FEATURE_FLAGS` for Preview. Only server code reads them; the root layout passes the PostHog key and host to the analytics client component as props.
- The PostHog provider is ours, `PostHogProvider` in `web/src/lib/flags/posthog.ts`, a thin OpenFeature provider over `posthog-node`. It does not use PostHog's `@posthog/openfeature-node-provider`, which sends one `/flags` request per flag and caches nothing. Ours asks `evaluateFlags` once for every flag of the identity, keeps the answer for 30 seconds per process, and serves each boolean evaluation from it. So the layout and any number of `isFlagEnabled` calls cost at most one PostHog request per process every 30 seconds, and a change in PostHog can take that long to show. The request times out after 500 ms and is not retried. If it fails, every flag is off until the cache entry expires, and the error is logged as `Feature flags could not be evaluated`. The flags code sends PostHog nothing else: no `$feature_flag_called` events. Page views and web vitals come from the browser; see [Analytics](#analytics).
- The in-memory list is an OpenFeature provider too (`web/src/lib/flags/memory.ts`), so both modes go through the same OpenFeature client.
- Within one request, `getFlagsOn` is memoised with React's `cache`, so the layout, server components and client components see the same values even if the 30-second entry expires mid-render.
- Rendering: in PostHog mode `getFlagsOn` calls `connection()` before asking PostHog, and the root layout calls `getFlagsOn`, so every page is rendered on each request, including pages that check no flag. That is the price of client components getting PostHog values in their first render, and it keeps a change in PostHog live without a deploy. It adds a server render per page view, not a PostHog request. In in-memory mode `getFlagsOn` does not call `connection()`, so pages stay static.
- Identity: flags are not evaluated per user, even when someone is signed in, and the app stores no visitor identifier, so the server evaluates every flag for one anonymous distinct ID, `anonymous`, and the browser uses the server's values. Both sides always agree, but a percentage rollout or person targeting is on or off for everyone. Per-user flags later mean evaluating for the user's distinct ID per request instead of reading the shared cache, and passing the result through the same `FlagsProvider`; callers do not change.
- The browser loads no OpenFeature code and makes no `/flags` request. The only PostHog code in the browser is the analytics client, fetched only when a PostHog key is set, with flags turned off. The layout sends the registered keys that are on, and only those, in the page payload, so anyone can read them in the page source: a flag key must not be secret.
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

## Analytics

We track anonymous page-path counts and web vitals, nothing else: no visitor identity beyond one page load, no search params, referrers, campaign data, location or device details, and nothing stored in the browser.

The web app sends these to PostHog from the browser. `web/src/lib/analytics/` holds it: `client.ts` starts PostHog and sends events, `payload.ts` decides what may leave the browser, `analytics.tsx` is the client component the root layout renders.

It uses the same `NEXT_PUBLIC_POSTHOG_KEY` and `NEXT_PUBLIC_POSTHOG_HOST` as the flags (see [Feature flags](#web)), read in one place, `web/src/lib/posthog/config.ts`. There is no separate switch and no feature flag: without a key the layout does not render `Analytics`, so the PostHog chunk is never fetched and nothing is sent. The `Analytics` component and `next/web-vitals` (about 4 KB gzipped) are still in every page's JavaScript, unused. That covers local development, tests and preview deploys. Setting the key in a Vercel environment turns analytics on there with the next build.

### What is sent

Two events, to `NEXT_PUBLIC_POSTHOG_HOST` at `/e/`, and no other request:

| Event | When |
| --- | --- |
| `$pageview` | the first render of a page and every client-side navigation to a different path. A navigation that only changes search params or the hash is not a page view. |
| `$web_vitals` | TTFB, FCP, LCP, CLS and INP, each once per full page load. |

Every property PostHog would send is dropped except these, by the `before_send` hook `onlyAllowed` in `payload.ts`, which rebuilds each event from an allow-list. Any other event (for example `$identify` or PostHog's own warning events) is dropped entirely. A PostHog upgrade that adds properties therefore cannot send them without a change to `payload.ts`.

| Property | Events | Why |
| --- | --- | --- |
| `token` | both | the project key; PostHog needs it to accept the event |
| `distinct_id` | both | random per page load (see below); PostHog needs one |
| `$process_person_profile` | both | always `false`, so PostHog creates no person |
| `$lib`, `$lib_version` | both | which SDK sent the event |
| `$current_url` | both | origin and path only |
| `$pathname` | both | the path |
| `$geoip_disable` | both | always `true`, so PostHog does not look up a location from the IP |
| `metric_name`, `metric_value`, `metric_rating` | `$web_vitals` | the metric, its value and `good`, `needs-improvement` or `poor` |
| `$web_vitals_<NAME>_value`, `$web_vitals_<NAME>_event` | `$web_vitals` | the shape PostHog's own web vitals capture uses; the event object holds `id`, `name`, `value`, `delta`, `rating` and `navigationType` |

Each event also carries its `uuid`, `event` name and `timestamp`. `$set` and `$set_once` are dropped. That means none of PostHog's usual browser properties (browser, OS, screen, viewport, language, time zone), no `$session_id`, no `$referrer` or `$referring_domain`, and none of the `$session_entry_*` properties (entry URL, referrer, UTM parameters and ad click IDs), which PostHog adds regardless of `save_referrer` and `save_campaign_params`.

Search params: `ALLOWED_SEARCH_PARAMS` in `payload.ts` is the list of search params that may appear in `$current_url`. It is empty, since no route reads search params. A future route that needs one in analytics adds its name there; a param on the list is part of the page, so changing it counts as a page view. The hash is never sent.

### Privacy settings

- **No cookies or storage:** `persistence: "memory"`. The distinct ID lives only as long as the page; a full reload, a new tab or a return visit is a new anonymous visitor, so visitor and retention numbers count page loads, not people. When it starts, PostHog checks whether `localStorage` works by writing and removing one constant key, `__mplssupport__`; it stores nothing and sets no cookie. Relaxing persistence to `localStorage` or `cookie` would need a consent decision first.
- **No person profiles:** `person_profiles: "identified_only"` and nothing calls `identify`, so every event is sent with `$process_person_profile: false`. Signing in does not change this: the app never identifies a signed-in user to PostHog, and the auth routes (`/auth/*`) are route handlers that render no page, so they send no page view; the callback's `code` and `state` could not be sent anyway, since no search params are allowed. Identifying users later means calling `posthog.identify` after sign-in and `posthog.reset()` on sign-out from `client.ts`, and allowing `$identify` and the properties it needs in `payload.ts`, and needs a privacy decision first.
- **Do Not Track and Global Privacy Control:** checked before PostHog is loaded (`navigator.globalPrivacyControl`, `navigator.doNotTrack`, `window.doNotTrack`, `navigator.msDoNotTrack`). A visitor who sends either gets no PostHog code and no requests. `respect_dnt` is also on inside PostHog.
- **Nothing captured automatically:** autocapture, PostHog's own page view and page leave capture, session recording, heatmaps, dead clicks, exceptions, surveys, product tours, conversations and web experiments are all off. The browser makes no `/flags` request (`advanced_disable_flags`), since flags are evaluated on the server.
- **No third-party scripts:** the bundle is `posthog-js/dist/module.slim.no-external`, which has no code to inject scripts, and `disable_external_dependency_loading` is set as well.
- **Location:** `$geoip_disable: true` on every event stops PostHog's GeoIP enrichment for these events. Disabling the GeoIP transformation in the project's data pipeline settings stops it for the whole project.
- **IP addresses:** PostHog still receives the request IP and records it as `$ip` unless the project's "Discard client IP data" setting is on. Turn it on when the project is created; it cannot be set from the client.

### How it is wired

- The root layout renders `<Analytics project={…} />` only when there is a key. `Analytics` starts PostHog in an effect, after hydration, with a dynamic `import()`, so PostHog is a separate chunk (about 52 KB gzipped) that is not part of any page's first-load JavaScript and is never fetched without a key or under Do Not Track.
- Page views: a child component reads `usePathname()` and `useSearchParams()` inside a `<Suspense fallback={null}>` boundary, as Next requires for `useSearchParams` on statically rendered pages, and calls `trackPageview` in an effect when either changes. `trackPageview` reduces the URL to origin, path and allowed params and skips a URL equal to the last one sent, so a change to other search params, and React strict mode's double effects in development, send nothing.
- Web vitals: `useReportWebVitals` with one module-level callback, so the reference never changes, as the Next docs require. Next asks `web-vitals` for soft-navigation metrics too; those (`navigationType: "soft-navigation"`) are dropped. FCP, LCP and TTFB are final when reported and are sent at once. CLS and INP can be reported again under the same `id` with a new value when the page is hidden and shown again, so they are held, latest value per `id`, and sent once when the page goes away (`pagehide` and the page becoming hidden, in either order). Each `id` is sent at most once. Web vitals are sent with `sendBeacon`, which survives the page unloading. Each is filed under the page it measured (`navigationURL`), reduced like page view URLs.
- Caveats: in browsers without soft-navigation support (all but recent Chrome), CLS and INP keep accumulating across client-side navigations and are filed under the first page of the visit. A page that the browser discards in the background without a `pagehide` (common on mobile) loses its CLS and INP.
- Failure: if the PostHog chunk fails to load, `init` throws or a `capture` throws, analytics goes quiet and rendering and navigation carry on. Events sent while the host is unreachable or blocked are lost; the browser logs the failed requests itself, the app logs nothing. One PostHog client exists in the browser; the server keeps its `posthog-node` client for flags only and sends no analytics.
- Content-Security-Policy: the app sets none. If one is added, `connect-src` must allow `NEXT_PUBLIC_POSTHOG_HOST`; no `script-src` entry is needed, since the PostHog code is served from our own origin.
- Reverse proxy: ad blockers commonly block `*.posthog.com`, which loses those visitors' events. A first-party proxy (PostHog's managed proxy, or Next or Vercel rewrites) avoids that; point `NEXT_PUBLIC_POSTHOG_HOST` at it. The server-side flags use the same host, so the proxy must forward `/flags/` as well as `/e/`. Not set up yet.
- Tests: `client.test.ts` and `analytics.test.tsx` mock `posthog-js/dist/module.slim.no-external`, `next/navigation` and `next/web-vitals`. `real-posthog.test.ts` runs the real PostHog client under jsdom with `fetch`, `XMLHttpRequest` and `sendBeacon` stubbed, and checks the decoded payloads: the exact property set of each event, no search params, hash or referrer anywhere, `/e/` as the only endpoint, no script element, no cookie and nothing left in storage. `client.ts` keeps its state in the module, so each test calls `vi.resetModules()` and imports it again.

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
| `AGENDA_SUMMARY_TIER` | `routine` | Tier the agenda summary job uses (see [Agenda summaries](#agenda-summaries)): `routine` or `strong`. Anything else stops the binaries at startup. |

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

`internal/storage` stores the audio track of meeting recordings in a [Railway bucket](https://docs.railway.com/storage-buckets), which speaks the S3 API. `storage.New(cfg.Storage)` returns a `storage.Store` with five methods: `Put` (streams from an `io.Reader`), `Get` (returns a streaming `io.ReadCloser`), `Delete` (a missing key is not an error), `Stat` (key, size, last modified) and `ListOlderThan` (keys last modified before a time, for the retention job). Keys are chosen by the caller. The `download_audio` job is the first caller (see [Recording audio](#recording-audio)). `Put` uploads in 16 MiB multipart parts, so an upload of unknown length buffers at most one part in memory; without a part size, `minio-go` sizes parts for a 5 TiB object and buffers about 537 MiB.

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

In production the `audio` bucket is defined in `.railway/railway.ts` and the API service's `STORAGE_*` variables reference it. Objects are meant to be deleted after about six months; the retention job that does it is not built yet (CIV-42), and Railway buckets have no lifecycle rules. The worker needs the same `STORAGE_*` variables to store audio; without them `download_audio` jobs are cancelled before anything is downloaded.

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

The worker checks every live agency once a day for board meetings, records them, stores the text of each published agenda, and downloads the audio track of each held meeting's recording. These are the first links of the pipeline (check agency, process agenda, summarize the agenda, download audio, transcribe, summarize the transcript). Four River jobs in `internal/jobs` do the checks, agendas and audio, behind the `ingest-check-agencies` feature flag; the audio download is also behind its own flag, `ingest-download-audio` (see [Recording audio](#recording-audio)). `summarize_agenda` and its hourly sweep `enqueue_agenda_summaries` summarize agenda versions behind their own flag (see [Agenda summaries](#agenda-summaries)).

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
5. In one transaction for the agency, it upserts each meeting with `store.UpsertMeetingTx` (unique on agency and source id, so a rerun updates rows instead of duplicating them; a changed title or start time updates the row) and hands each one to the follow-up hook, which inserts a `process_agenda` job for every meeting with an agenda URL and, with `ingest-download-audio` on, records the recording and inserts a `download_audio` job for every held meeting with a recording URL. Any error rolls back the whole agency, including the jobs it inserted.

`process_agenda` downloads one meeting's agenda and stores its text as an agenda version (see [Agendas](#agendas)). Its args are the meeting id and the agenda URL. It is unique by args, queue and UTC day, like `check_agency`, so a meeting's agenda is read at most once a day per URL. Every daily check enqueues it again for every meeting in the window that has an agenda, because an agency can replace an agenda without changing its URL; an unchanged agenda stores nothing. The job has a 5 minute timeout and at most 5 attempts.

Two things are deliberately simple for now. Agenda processing has no flag of its own: it shares `ingest-check-agencies`, so turning that off stops both the checks and the agenda downloads. And every agenda in the 120-day window is downloaded again every day, with no conditional GET (`If-None-Match`, `If-Modified-Since`) and no per-host rate limit; for the agencies registered today that is a handful of documents per agency per day.

`process_agenda` does this, in order:

1. With the flag off, it completes without doing anything.
2. It fetches the agenda URL through `jobs.Config.Fetcher`, with the default `FETCH_MAX_BYTES` cap. The worker passes plain HTTP with the Firecrawl fallback for bot-blocking statuses, the same composition as `fetch.New(cfg.Fetch)` that the adapters use. Without a fetcher the job is cancelled with `jobs.ErrNoFetcher`.
3. It extracts and normalizes the text through `jobs.Config.Extractor` (see [Agenda text](#agenda-text)). The URL tells the extractor what to expect: a URL whose path ends in `.pdf`, in any case, must not come back as an HTML page.
4. If the response was a bot check or error page (`extract.ErrBlocked`: HTML for a `.pdf` URL, or a page with a known interstitial marker), it fetches the URL again through `jobs.Config.Fallback` (the worker passes the Firecrawl fetcher, which returns markdown) and extracts that. Without Firecrawl configured, or when Firecrawl gets a bot check too, nothing is stored and the job fails so that River retries it.
5. In one transaction, it adds the version with `store.AddAgendaVersionTx`, hashing the normalized text with `store.HashContent`. When the text equals the latest version's, nothing is stored, no event is recorded, and the job completes. When a version is created, the store records the `agenda_published` (version 1) or `agenda_changed` event in that transaction (see [Meeting events](#meeting-events)), and the job then calls the agenda follow-up hook in the same transaction; an error there rolls back the version and its event.

Errors of `check_agency`:

- Retryable: `sources.Retryable(err)`, which is `fetch.IsRetryable` (network failures, timeouts, 408, 429, 5xx) plus a canceled context or exceeded deadline, including the job's own timeout. The job returns the error and River retries it with backoff. Database and follow-up errors are retried the same way.
- Permanent: everything else, such as a 404 or other 4xx, `fetch.ErrTooLarge`, markup the adapter cannot parse, or `sources.ErrInvalidMeeting`. The job is cancelled with `river.JobCancel`; the next day's run tries again.

One agency's failure never affects another: each agency is its own job and its own transaction.

Errors of `process_agenda`, classified the same way:

- Retryable: `sources.Retryable(err)` for the fetch and for the extraction (a canceled context or the job's own timeout while the PDF extractor runs), `extract.ErrBlocked`, and database and follow-up errors. River retries with backoff; a page that is still blocked after 5 attempts is discarded and the next day's check enqueues it again.
- Permanent, cancelled with `river.JobCancel`: other fetch errors (404 and other 4xx, `fetch.ErrTooLarge`), `extract.ErrUnsupported`, `extract.ErrMalformed`, `extract.ErrNoText`, `extract.ErrLimitExceeded`, `extract.ErrUnavailable` (the PDF extractor binary is missing), and a meeting that no longer exists (`store.ErrNotFound`). The next day's check enqueues the agenda again.

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

Adapters are registered by agency slug in `cmd/worker/adapters.go`, a map from slug to `sources.Constructor` (`func(sources.Deps) sources.Adapter`). At startup the worker builds the registry with `sources.NewRegistry(deps, adapters)`, which calls each constructor once with `sources.Deps{Fetcher, Clock, Logger}`: plain HTTP with the Firecrawl fallback (the composition `fetch.New(cfg.Fetch)` returns; the worker builds it from its parts so it can also give the Firecrawl fetcher to `process_agenda`), `sources.SystemClock{}` and the worker's logger. `Deps.Log()` returns the logger, or one that discards everything when none is set, as in most tests. Adapters get their dependencies only from `Deps`, never from globals. Registered today: `la-metro` and `octa` (the Legistar adapter below) and `metrolink` (the Granicus adapter below) and `vctc` (the VCTC adapter below). Every other agency is skipped until its adapter lands. The worker logs the registered slugs when it starts.

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
	Client:               "metro",
	BodyIDs:              []int{138, 199},
	TimeZone:             "America/Los_Angeles",
	Granicus:             legistar.Granicus{Host: "metro.granicus.com", ViewID: 2},
	FinalMinutesMeanHeld: true,
}

var OCTA = legistar.Config{
	Client:             "octa",
	BodyIDs:            []int{138},
	TimeZone:           "America/Los_Angeles",
	Granicus:           legistar.Granicus{Host: "octa.granicus.com", ViewID: 2},
	CancelledInComment: true,
}
```

| Field | Meaning |
| --- | --- |
| `Client` | The Legistar client name, the path segment after `/v1/`. |
| `BodyIDs` | The `EventBodyId`s of the full board: regular and special board meetings, no committees. |
| `TimeZone` | IANA zone of the agency's wall-clock times. |
| `Granicus` | Host and `view_id` of the agency's Granicus media player, for the recording URL. Leave it empty if the agency has none. |
| `FinalMinutesMeanHeld` | Whether minutes status `Final` is evidence that a past meeting took place. True for LA Metro. False for OCTA, whose `Final` does not mean minutes exist: cancelled board meeting 1460 (2016-07-11) is `Final`, and board meeting 2906 (2026-11-09) has been `Final` with no minutes file since its last edit on 2026-11-06. |
| `CancelledInComment` | Whether a cancellation in `EventComment` counts. True for OCTA, which writes short codes there (`CANCELLED`, `REVISED`, `SPECIAL`, `SPECIAL MEETING`, and on hidden events training notes). False for LA Metro, which writes multi-line public comment instructions there (44 of 127 board events) that can mention a cancelled item without the meeting being cancelled. |
| `BaseURL` | Defaults to `https://webapi.legistar.com`. Tests point it elsewhere. |
| `PageSize` | Defaults to 1000, Legistar's largest page. |

To add a Legistar agency:

1. List its bodies: `GET https://webapi.legistar.com/v1/{client}/bodies`. Pick the full board's ids by name, not by `BodyTypeName`: "Primary Legislative Body" also covers test bodies, workshops and public hearings. Skip the `(SAP)` bodies, which are Spanish audio copies of the same meetings.
2. Add a `Config` next to `LAMetro` in `internal/sources/legistar/agencies.go`, and register `legistar.Constructor(cfg)` under the agency's slug in `cmd/worker/adapters.go`. `Constructor` panics on an invalid config, so a bad config stops the worker at startup; `legistar.New` returns the error instead.
3. Record one real events response (step 1's GET, with our User-Agent), trim it, save it under `internal/sources/legistar/testdata/`, and add a test like `TestLAMetroBoardMeetingsFromRecordedResponse`.

For LA Metro, body 138 is "Board of Directors - Regular Board Meeting" and 199 "Board of Directors - Special Board Meeting". Not included: the committees, the `(SAP)` copies, the service councils, the taxpayer oversight committees, the public hearings, the test bodies (300 to 303), and the board workshops ("Special Board Budget Workshop" 306, "Special Board Member Workshop" 310).

For OCTA, body 138 "Board" is the full board, and the only body whose name mentions a board. Checked against all 1,185 OCTA events from 2015-01-05 to 2026-11-25 on 2026-11-18. Kept: the 279 events of body 138, which include special meetings (marked only by `EventComment` `SPECIAL` or `SPECIAL MEETING`, such as 2444 on 2021-11-01) and a budget workshop recorded as its own event (2198 on 2019-05-13, with no `EventTime`, beside regular meeting 2099 the same day). Dropped: the 3 hidden events of body 138, which are training and test meetings (2536 "GRANICUS TESTING", 2543 "Jordan's Agenda for Training", 2665 "For Training Only", all with agenda status `Hidden`), and every other body: Executive (139), Finance and Administration (140), Legislative (172), Regional Transportation Planning (173), State Route 91 Advisory (174) and Transit (175) Committees, the inactive Transportation 2020 Committee (183), "Authors" (191), and the citizens', technical and taxpayer oversight committees and subcommittees (205 to 213, 220, 221).

How the adapter reads Legistar:

- **Query.** One `GET /v1/{client}/events` with `$filter=EventDate ge datetime'{from}' and EventDate le datetime'{to}' and (EventBodyId eq … or …)`, `$orderby=EventDate,EventId`, `$top` and `$skip`. `{from}` and `{to}` are the window's ends as dates in the agency's zone, since `EventDate` is a local date. It pages with `$skip` until a page is shorter than `$top`; an event repeated across pages is returned once, and a full page with no new events, or with no event whose id can be read, is a malformed response. Events of other bodies are dropped in code as well.
- **Ids and titles.** `SourceID` is `EventId`, Legistar's key for the event row. The row is created once and edited in place: when OCTA revises an agenda (`EventComment` `REVISED`, agenda status `FINAL-REVISED`, a new `EventAgendaLastPublishedUTC`), when the clip is linked after the meeting, and when it is cancelled, the `EventId` stays and the agenda file name is built from it (`2906_A_Board_26-11-09_Agenda.pdf`). It does not depend on the position or order of events in the response. `Title` is `EventBodyName` (OCTA's is just "Board").
- **Hidden events.** An event whose `EventAgendaStatusName` is `Hidden` is not on the agency's public calendar; OCTA uses it for training and test meetings. It is dropped with an info log. An event of a body outside `BodyIDs` whose name contains "board" is also logged when dropped.
- **Time.** `EventDate` holds the local date (its time part is always midnight) and `EventTime` the local wall-clock time as text, such as `10:00 AM`. The two are joined in the agency's zone and converted to UTC, so daylight saving time is applied for that date. Runs of spaces are collapsed first (OCTA's older events have `09:00  AM`). Accepted time forms are `3:04 PM`, `3:04PM` and `15:04`, in either case. A missing, empty or unparseable `EventTime` gives local midnight of the date: the date is right, and when the agency fixes the time the next check moves the meeting. An unparseable one is logged as a warning. The agenda file name carries a date too (`_26-11-09_Agenda.pdf`); when it differs from `EventDate` the adapter logs a warning and keeps `EventDate`, the field the clerk edits. All 279 of OCTA's board events and LA Metro's recorded ones agree.
- **Status.** `cancelled` when `EventAgendaStatusName` contains "cancel" in any case (LA Metro writes `Canceled`, OCTA `CANCELLED`), or, when `CancelledInComment` is set, when the trimmed `EventComment` equals or starts with `CANCELLED` or `CANCELED` in any case (of OCTA's 6 cancelled board meetings, 5 say so only there, all as exactly `CANCELLED`). Otherwise `held` when the start time has passed and there is evidence the meeting took place: a recording (`EventMedia` or `EventVideoPath`), a minutes file (`EventMinutesFile`), or, when `FinalMinutesMeanHeld` is set, minutes status `Final`. Otherwise `scheduled`, which includes a past meeting with no such evidence. A past date alone is not evidence: LA Metro's minutes stay `Draft` with no file in Legistar, so for LA Metro the recording is the evidence. For OCTA too only a linked clip (or a minutes file, which OCTA has never set) makes a meeting `held`: a past OCTA meeting with no clip stays `scheduled`, as do the 46 board meetings of 2015 and 2016, which have no clip.
- **URLs.** `PageURL` is `EventInSiteURL`. `AgendaURL` is `EventAgendaFile`, empty until the agenda is published. `RecordingURL` is `EventVideoPath` when set; otherwise, when `EventMedia` (a Granicus clip id) is set and `Granicus` is configured, `https://{host}/MediaPlayer.php?view_id={view}&clip_id={EventMedia}`; otherwise empty. It is set only when the start time has passed and the meeting is not cancelled: the evidence of a recording is a clip that Legistar has linked to a meeting that has started. A clip id alone is not, since a clip can be linked ahead of the meeting for the live stream. The adapter never fetches the player page or the media; resolving the media file belongs to the recording download, and OCTA's media can appear days after the meeting.
- **Upcoming meetings.** OCTA creates a board event in Legistar when it posts the agenda, a few days before the meeting (the 2026-11-23 meeting was not in Legistar on 2026-11-18), and LA Metro lists its calendar ahead. Both are returned as soon as they are in the window, as `scheduled`, with the agenda URL when there is one. The agenda PDF keeps its URL when revised.
- **Errors.** Fetch errors are wrapped with `%w` and keep their retryability. A body that is not a JSON list is `legistar.ErrMalformed`, which is permanent. An event that cannot be read (not a JSON object of the expected types, or without an id, a parseable date, a body name, an absolute `EventInSiteURL`, or with a relative agenda or video URL) is skipped with a warning, and the other events are kept. The check fails with `ErrMalformed` when no event in the response decodes, or when every board event in it is unreadable: that is what a change in Legistar's format looks like. An item that does not decode cannot be told apart as a board event, so it fails the check only when nothing decodes; when other events decode, even if they are all hidden or of other bodies, the format is intact and the item is skipped like any other. A skipped event produces only a `WARN` log line and the check still succeeds, so a partial format change, such as new events missing a field while older ones still parse, is visible only in those warnings. If Legistar answers 401, 403, 406 or 429 and Firecrawl is configured, `fetch.New` falls back to Firecrawl, whose markdown is not JSON, so that check is cancelled as malformed and the next day's check tries again.

The daily check calls only the API host, which has no `robots.txt`. The agenda hosts `metro.legistar1.com` and `octa.legistar1.com` and the player hosts `metro.granicus.com` and `octa.granicus.com` disallow crawlers; following CIV-62 they may be fetched once a day with an identifying User-Agent. The check does not touch them; the later stages that do identify themselves with `FETCH_USER_AGENT` (see [Fetching](#fetching)).

The fixtures in `internal/sources/legistar/testdata` are real LA Metro responses recorded on 2026-11-17 and trimmed to a few events and fields: `metro_board_events.json` (the board-only query for 2026-01-01 to 2027-01-31) and `metro_events_with_committee.json` (a board meeting and a Planning and Programming Committee meeting from an unfiltered query). LA Metro had no upcoming board meeting without an agenda that day, so that case, the time edge cases and the status edge cases use events built in the tests.

The OCTA fixtures are real responses recorded on 2026-11-18, without `EventItems` and `EventRowVersion`: `octa_board_events.json` (every board event from 2026-08-01, 2900 to 2906: one cancelled, one revised, all with agendas), `octa_board_history.json` (eight older board events: cancelled by comment only, special, the null-time budget workshop, the double-spaced time, and the three hidden training events) and `octa_events_with_committees.json` (every event of any body from 2026-11-12, upcoming committee meetings with agendas and cancelled committees, no board meeting). OCTA had no upcoming board event that day, so `TestOCTASourceIDsAreStableAcrossDailyRuns` derives the earlier states of meeting 2906 (agenda posted, agenda revised, the morning of the meeting) and a next meeting 2907 from the recorded 2906, feeds the states in changing order and checks every `SourceID` keeps its meeting.

### Granicus

`internal/sources/granicus` reads any agency that publishes its meetings through a Granicus `ViewPublisher` page, from that view's two RSS feeds: `https://{host}/ViewPublisherRSS.php?view_id={view}&mode=agendas` and `&mode=vod`. One agency is one `granicus.Config`:

```go
var Metrolink = granicus.Config{
	Host:       "metrolink.granicus.com",
	ViewID:     8,
	BoardNames: []string{"Board of Directors", "Board of Directors - Special Meeting", "Special Board of Directors", "Board Meeting", ...},
	TimeZone:   "America/Los_Angeles",
	StartTime:  "10:00",
}
```

| Field | Meaning |
| --- | --- |
| `Host` | The agency's Granicus host, with no scheme, port or path. |
| `ViewID` | The `view_id` of the agency's publisher page. |
| `BoardNames` | Meeting names of the full board, matched whole after the title is cleaned (below), without regard to case or repeated spaces. The matched entry, as written here, is the meeting's `Title` and the start of its `SourceID`. |
| `TimeZone` | IANA zone of the agency's wall-clock times. |
| `StartTime` | The board's usual start, `15:04`, local. Upcoming meetings carry no start time in the feed, so they start at this time until the archived clip gives the real one. |

To add a Granicus agency, find the `view_id` in the `ViewPublisher.php` iframe on its board page, list the meeting names in its agenda feed, add a `Config` next to `Metrolink` in `internal/sources/granicus/agencies.go`, register `granicus.Constructor(cfg)` under the agency's slug in `cmd/worker/adapters.go`, and record both feeds as fixtures. `Constructor` panics on an invalid config; `granicus.New` returns the error.

How the adapter reads Granicus:

- **Feeds.** Two GETs per check: the agenda feed, then the video feed. Neither takes a date filter. The agenda feed lists the view's latest 100 archived clips (link `AgendaViewer.php?view_id={view}&clip_id={clip}`) and, on hosts that have any, the upcoming meetings whose agenda is published (link `AgendaViewer.php?view_id={view}&event_id={event}`). The video feed lists the same clips (link `MediaPlayer.php?view_id={view}&clip_id={clip}`) and no upcoming meetings; event items in it are ignored. Metrolink listed no upcoming meetings on 2026-11-18; the event shape is taken from Sacramento's feed (`testdata/sacramento_agendas_upcoming.xml`).
- **Titles.** An item title is `{name} - {Mon DD, YYYY}`, and that date is the meeting's local date. The name is cleaned before it is matched: a leading `*`, a cancel marker at either end (`CANCELLED - `, `Cancelled: `, ` - CANCELLED`, ` (Cancelled)`), a trailing correction marker (` - CORRECTED AGENDA`, ` - Revised`, ` - Amended`, ` - Updated`) and a trailing parenthesis such as ` (9/9/11)` are removed. Items whose cleaned name is not in `BoardNames` are dropped; a dropped title that contains "board" is logged at info level so a missed board meeting is visible.
- **Board names for Metrolink.** Checked against all 653 clips on Metrolink's publisher page on 2026-11-18. Kept: "Board of Directors" (the only name since 2023), and the older full-board names "Board Meeting", "Metrolink Board Meeting", "Special Board Meeting", "Special Board of Directors", "Board Meeting - Emergency", "Board Meeting and Workshop", "Board Meeting and Budget Workshop", "Special Board Meeting and Annual Workshop", "Special Board of Directors' Annual Strategic Session" and "Board of Directors' Annual Strategic Work Session and Meeting", plus "Board of Directors - Special Meeting" for a special meeting named the current way. Dropped: committees, stand-alone board workshops ("Board Workshop", "Special Board Meeting - Safety Leadership Workshop", as LA Metro's workshops are), and single-document clips ("Board Meeting - Item No. 8 - Attachment A", "Metrolink Board Meeting (03/09/12) Item 13 - Document No. 1 CAFR").
- **One meeting per board per day.** All items of one board name on one local title date are one meeting: upcoming events, revised agendas, the archived clip, corrected-agenda clips and the video items. There is no numbering and no splitting, so the same items always give the same meeting whatever order they arrive in or which of them the feed still lists. In Metrolink's 653 archived clips no board name repeats on one day except as a correction clip. Different names stay different meetings: "Special Board of Directors" and "Board of Directors" on one day are two. When clips of one meeting have `pubDateParts` with different start times, the adapter logs a warning with the clip ids, so a genuine second session would be visible; it is still stored as the same meeting.
- **Ids.** `SourceID` is `{board}/{date}`, such as `board-of-directors/2026-12-11`: the matched board name in lower case with every run of other characters turned into `-`, and the local date from the title. An upcoming event and the clip it becomes have different Granicus ids but the same title, so they share a `SourceID` and the upsert updates one row. A clip whose title has no date takes the date from its `pubDateParts`. The RSS `guid` is not used.
  - The id depends on the name and the date in the title. If the clerk renames a meeting between its event and its clip, or re-dates an upcoming event, the next check stores a second row under the new id and the first row stays behind as a stale `scheduled` meeting. Nothing in this story removes it; the store has no way yet to retire a meeting a source no longer lists.
- **Merging.** Items are ordered events first, then clips, each by id, so the order is the same on every run. Within one meeting:
  - the agenda URL and the cancel marker come from the last agenda-feed item in that order: the newest clip, or before the meeting is archived the newest event, so a revised agenda or a corrected-agenda clip wins;
  - the recording comes from the first video item with an enclosure (the lowest clip id);
  - the start time comes from the first clip whose `pubDateParts` falls on the title's date (below).
- **Time.** For a clip, from `gran:pubDateParts` (`yr`, `mo`, `day`, `hr`, `min`), read as wall-clock time in the agency's zone and converted to UTC, but only when that date equals the title's date. Two of Metrolink's 100 live clips have parts on a later day: "Board of Directors - Mar 28, 2025" has `2025-04-01 09:15`, and "Board of Directors - Jan 26, 2024" has `2024-01-30 09:15`. Such a clip, or one with missing or impossible parts, starts at `StartTime` on the title's date. For an upcoming event, `pubDateParts` is when the agenda was published, not the meeting time (Sacramento's 2026-11-19 meeting has `day='12'`), so the start is also the title's date at `StartTime`. The upsert overwrites `starts_at`, so the archived clip corrects it. The RSS `pubDate` is never used: its offset is always `-0800` and its hour is sometimes on a 12-hour clock (Metrolink's 2:30 PM meeting on 2026-10-09 has `02:30:00 -0800`).
- **Status.** `cancelled` when the meeting's agenda item (as chosen above) carries a cancel marker; with no agenda item, the last video item decides. Otherwise `held` when the start time has passed and there is a recording URL. Otherwise `scheduled`.
- **URLs.** `AgendaURL` is the chosen agenda item's link: a clip's `AgendaViewer.php?...&clip_id=`, or an event's `AgendaViewer.php?...&event_id=` before the meeting is archived. Both redirect to the agenda PDF. `RecordingURL` is the video feed's `MediaPlayer.php` link, set only when that item has an `<enclosure>` and the meeting is not cancelled. `PageURL` is the recording URL, else the agenda URL, else the first item's link. The adapter never fetches the agenda, the player page or the media.
- **What "held" means in practice.** Every video item seen so far has an enclosure (`DownloadFile.php?...&clip_id=`, typed `video/x-ms-wmv`): all 100 of Metrolink's and all 100 of Sacramento's on 2026-11-18, including Sacramento's clip for a cancelled meeting. So the enclosure shows a clip exists, not that it holds media, and `held` in effect means "an archived clip whose start has passed". Whether there is media is settled by the recording download, which reads the media file's uuid from the player page (it can differ from the RSS `guid`).
- **Errors.** Fetch errors are wrapped with `%w` and keep their retryability. A body that is not RSS with a channel is `granicus.ErrMalformed`, which is permanent; this covers a Firecrawl fallback answer (markdown), as for Legistar. A board item that cannot be read (its link is not an absolute URL with a numeric `clip_id` or `event_id`, or it has no date to build its id from) is skipped, not fatal:
  - Outside the window it is logged at debug level. The feed keeps 100 items, so failing on one would block the check for months.
  - Inside the window, or with no date to place it, it is logged at warn level and the other meetings are still returned. One bad item must not keep the agency's other meetings, and their agendas, from being stored.
  - Board items in the window are counted in three kinds: archived clips in the agenda feed, upcoming events in the agenda feed, and items of the video feed. If a kind has items in the window and every one of them is unreadable, the check fails with `ErrMalformed`: that is what a change in Granicus's link or title format looks like, and it can hit one kind alone (only the video links, or only upcoming links). A kind with no items in the window is fine. An unreadable agenda item is counted as a clip or an event by the `clip_id` or `event_id` in its link; with neither, as a clip when its date has passed and as an event otherwise.

`metrolink.granicus.com` serves `User-agent: * / Disallow: /` in `robots.txt`. Following the decision in CIV-62, the daily check fetches the two feeds once a day with `FETCH_USER_AGENT` (see [Fetching](#fetching)).

Fixtures in `internal/sources/granicus/testdata`, recorded on 2026-11-18 and trimmed:

- `metrolink_agendas.xml` and `metrolink_vod.xml`: Metrolink's two feeds, seven items each: the first six (five Board of Directors meetings from 2026-07-24 to 2026-11-13 and one Budget, Audit, and Finance Committee meeting) and clip 1304, "Board of Directors - Mar 28, 2025", whose `pubDateParts` say 2025-04-01.
- `sacramento_agendas_upcoming.xml`: the City of Sacramento's agenda feed (`sacramento.granicus.com`, view 21), a different agency used only for the shape of upcoming items: two upcoming events (`event_id` 6450 and 6443), an archived clip and a cancelled clip.

Other cases (corrections, cancellations, several items for one meeting across successive runs, unreadable items) use feeds built in the tests.

### VCTC

`internal/sources/vctc` reads the Ventura County Transportation Commission from its own WordPress site, `www.goventura.org` (not `vctc.org`, which is unrelated). Each meeting is a post of the custom type `meeting`, served by the WordPress REST API with no authentication. The site is not a shared platform, so the package is named after the agency, but the agency's values are still one `vctc.Config`:

```go
var VCTC = vctc.Config{
	Host:           "www.goventura.org",
	Boards:         []vctc.Board{{TypeID: 25, Name: "Commission Meeting"}, {TypeID: 33, Name: "Special Meeting"}},
	CommitteeWords: []string{"TRANSCOM", "TTAC", "CTAC", "SSTAC", "HVPAC", "HVTAC", "CEPAC", "CETAC", "SPBLAC", "CALVANS", "Committee"},
	TimeZone:       "America/Los_Angeles",
	StartTime:      "09:00",
}
```

| Field | Meaning |
| --- | --- |
| `Host` | The site's host, with no scheme, port or path. |
| `Boards` | The full board's `meeting_type` term ids, each with the meeting name its post titles start with. |
| `CommitteeWords` | Words that, as a whole word of the agenda link's file name or text in any case, mark a committee's agenda filed under a board type. |
| `TimeZone` | IANA zone of the agency's wall-clock times. |
| `StartTime` | The board's usual start, `15:04`, local, for a post without a readable time. |
| `PageSize` | Posts read per check. Defaults to 20, at most 100. |
| `YouTube` | The channel whose videos are the recordings (below). Empty, as for VCTC today, means no recording URLs. |

**Recordings are off for VCTC, pending a decision on YouTube** (below), so VCTC meetings carry agendas only: no `RecordingURL`, no audio download.

How the adapter reads the site:

- **Query.** One `GET /wp-json/wp/v2/meeting?meeting_type=25,33&per_page=20&orderby=date&order=desc&_fields=id,link,title,content,meta,meeting_type`: the 20 newest posts of the board's types, by publish date. VCTC creates a post when it publishes the agenda, a few days before the meeting (the 2026-11-06 meeting's post is from 2026-10-30), so 20 posts reach well past the 30-day look-back of a monthly board. Posts of other types are dropped in code as well.
- **Ids.** `SourceID` is the WordPress post id, such as `24950`. The post is created once and edited in place: when the agenda is amended (the 2026-10-09 meeting's post now links `AMENDED-AGENDA-COMMISSION-10.9.26.pdf`), when presentations are added after the meeting, and when the title gets `(Cancelled)`. It does not depend on the title, the date, or the order of posts in the response. Two posts for one board on one day are two meetings; the adapter logs a warning.
- **Board only.** A post is kept when all its `meeting_type` terms are configured board types, its title, cleaned as below, starts with that type's name before the ` – ` separator, and its agenda link does not name a committee (`CommitteeWords`). Type 25 ("Commission") holds the full commission, plus one misfiled committee agenda; type 33 ("Special Meeting") also holds hearings, workshops and studies, so only titles named `Special Meeting – {month}` are kept. Checked against all 208 posts of the two types on 2026-11-18 (181 of type 25 from 2010 to 2026, 27 of type 33):
  - Kept 188: 180 commission meetings, including the `(Special Meeting)` and `(Cancelled)` titles, and the 8 special meetings of 2015 to 2023, whose agendas are special meetings of the Commission.
  - Dropped 1 of type 25: post 1126, `Commission Meeting – December 2021 (2)`, whose only document is `AGENDA-PACKET-December-8-2021-TRANSCOM.pdf`, the TRANSCOM committee's agenda for Wednesday 2021-12-08 filed under the commission type. The `(2)` is WordPress's suffix for a second post with the same title, not a second commission meeting; the commission met on 2021-12-03 (post 1127).
  - Dropped 19 of type 33: the goal-setting session (a workshop, as for LA Metro), the SPBL community meeting, the TIES study meetings, the ADA ad hoc committees, the unmet transit needs hearings, a Title VI hearing notice, and `Admin/Finance – August 2021 (Special Meeting)`, which is also tagged with the Admin Finance committee type (30).
  - Committees (TRANSCOM, TTAC, CTAC-SSTAC, HVPAC, Admin Finance, CEPAC, CETAC, SPBLAC, Calvans) have their own types. No kept post's agenda link contains a committee word.
  - A dropped post of a board type whose title still mentions the commission, a board or a special meeting is logged as a warning, and so is a post dropped for a committee agenda, so a renamed or misfiled board meeting is visible; other dropped titles that look like a board meeting are logged at info level.
- **Titles.** `Title` is the post title with HTML entities decoded, such as `Commission Meeting – November 2026`. Before matching, a cancel marker is removed: a whole trailing `(Cancelled)` or `(Canceled)`, a trailing ` - Cancelled`, or a leading `Cancelled` (also `**CANCELLED**`, `Cancelled:`), in any case. A cancel word elsewhere, or inside a longer parenthesis, does not cancel the meeting. Other trailing parentheses (`(Special Meeting)`, `(2)`) are removed too. The text after the last ` – ` is read as a month (`November 2026`, `Nov 2026`, `Sept. 2026`) or a day (`December 4, 2026`), with full month names and the abbreviations `Jan` to `Dec` and `Sept`, in any case. Anything else (`Fall 2026`) is no title date.
- **Time.** The meeting date is `meta._meeting_date` (`2026-11-06`) and the time `meta._meeting_time` (`09:00`, also accepted as `9:00 AM`), joined in the agency's zone and converted to UTC, so daylight saving time is applied for that date. These fields are the meeting's own, set by the clerk; the publish date is not the meeting date. When the time is empty the start is `StartTime` with an info log; when it does not parse, `StartTime` with a warning. The title's month or day is checked against the date: when they differ the adapter logs a warning and keeps `_meeting_date`. All five posts with a meeting date (2026-04-03 to 2026-11-06) agree with their titles.
- **Posts without a meeting date.** The clerk does leave `_meeting_date` empty: of the 208 posts only five have it, and the May and June 2026 meetings (posts 23052 and 23053) have none. When it is empty or does not parse, the adapter looks for one full date in the agenda link, its file name and its text: `November-6-2026`, `Nov 6, 2026`, `2026.11.06`, `11.6.26`, `11.6.2026` or `08-20-2021`. It uses that date only when exactly one distinct date is found there and it lies in the title's month and year (or equals the title's day), and then starts the meeting at `StartTime` with a warning naming the file. A title without a month, a link with no date, a date of another month, or two dates of the month (`…-08.15.2023_…-Updated-08.11.2023.pdf`) give no date, and the post is skipped with a warning (debug when the title's month is outside the window); it is never dated from the publish date or a guess. The rest of the post's text is not used: besides the agenda and supporting documents it holds the site's "Meetings & events" list, which shows another meeting's date. Over the 208 recorded posts the fallback dates 91 of the board posts, every board meeting since December 2020 except three (`VCTC-JUNE-72024-AGENDA-1.pdf`, `OCTOBER-6-AGENDA.pdf`, `…-DECEMBER-162021.pdf`) and a few older ones; most older file names carry only a month (`March-2020-Agenda.pdf`). It dates none wrongly: each of the 91 dates is a Friday, the commission's meeting day, and the five with a `_meeting_date` get the same date from both. Without the board filter it would also date 1126 (2021-12-08, the TRANSCOM meeting it is) and two dropped type-33 posts.
- **Agenda.** `AgendaURL` is the first link under the post's agenda heading, up to the next heading. The heading is the one with `id="current-agenda"` (the site's current markup) or whose text, in any case and without punctuation, is `Agenda` with any of `Current`, `Amended`, `Revised` or `Updated` before it (`Current agenda`, `Current agenda:`, `AGENDA`, `Amended Agenda`). All 208 posts have exactly one link there, labelled `Agenda` or `Amended Agenda`; supporting documents and presentations are under their own headings. With several links the one whose text or file name says amended or revised wins, else the first. A relative link is resolved against the post; a link that is not `http` or `https` is ignored with a warning. An amended agenda is a new file name on the same post, so the next check enqueues `process_agenda` for the new URL. When a board post that is not cancelled has no agenda link and its meeting starts within 72 hours or has passed, the adapter logs a warning, so a changed heading cannot make agendas vanish silently.
- **Status.** `cancelled` when the title carries a cancel marker. Otherwise `held` when the start has passed and there is evidence the meeting took place: a link under a `Presentations` heading, or a matched recording (off for VCTC). Otherwise `scheduled`; a past date alone is not evidence. The site's current posts get a `PRESENTATIONS` heading with the slides shown at the meeting. In the recorded history three posts have it, the 2026-09-11, 2026-10-09 and 2026-11-06 meetings: each post was last edited after the meeting had started (11:47 and 11:42 local on the meeting days, and 2026-11-09), and each of the three meetings has its own video on the channel, so all three did take place. No cancelled post has presentations. Older posts list slides among the supporting documents, where they cannot be told from documents posted with the agenda, so they are not evidence, and the meetings of July 2026 and before stay `scheduled` (they are outside the 30-day look-back anyway). A meeting whose slides were posted under that heading ahead of the meeting becomes `held` once its start has passed, unless it is cancelled.
- **Recordings.** VCTC posts its recordings only on YouTube (channel `UCer8W37pepfQH8mhDbTBRjg`, `@VCTC001`); the meeting post has no video link. The channel's feed, `https://www.youtube.com/feeds/videos.xml?channel_id={channel}`, lists the 15 newest videos with their publish time. The evidence rule: a video is a meeting's recording when its title starts with `TitlePrefix` (`VCTC Commission Meeting`, in any case), it was published at or after the meeting's start and less than `MaxDelay` (72 hours) after it, no other non-cancelled board meeting in the result also started in that span before the publish time, and no other such video matches the same meeting. Anything ambiguous gets no recording and a warning. The date in the video title is never used: the 2026-11-06 meeting's video is titled `November 11, 2026`, which is only logged. Of the six meeting videos in the feed on 2026-11-18, every one was published between 13 hours 42 minutes and 14 hours 28 minutes after its meeting's start. `RecordingURL` is `https://www.youtube.com/watch?v={id}`, set only for a meeting that has started and is not cancelled. A special meeting's video would need another title prefix, so special meetings get no recording.
- **Why recordings are off.** `youtube.com/robots.txt` disallows `/feeds/videos.xml` for every crawler, and the CIV-62 decision covers the Legistar, Granicus and PrimeGov hosts, not YouTube. So `vctc.VCTC` has an empty `YouTube`, the daily check never contacts YouTube, and VCTC meetings have agendas only. The matching is built and tested with `vctc.VCTCYouTube`; to turn it on once fetching the feed is approved, set `YouTube: VCTCYouTube` in `internal/sources/vctc/agencies.go`. Downloading the audio also needs yt-dlp, which belongs to the recording story.
- **Errors.** Fetch errors are wrapped with `%w` and keep their retryability. Content problems in single posts are skipped, never fatal: a post that does not decode (not an object of the expected types) and a board post that cannot be used (no positive id, a link that is not absolute, no `meta` object, or no meeting date that can be established) are skipped with a warning, or at debug level when the title's month is outside the window. `vctc.ErrMalformed`, which is permanent, means the response's shape changed:
  - the body is not a JSON list (this covers a Firecrawl fallback answer, which is markdown, as for Legistar);
  - the list has posts and none of them decodes;
  - the list has posts and none of them is a well-formed board post: a board type and title, a positive id, an absolute link, and a `meta` object with a string `_meeting_date` field, empty or not.

  The page is the newest posts of the board's own types, so it always holds board posts; when none of them has those fields, a field was renamed or dropped, or the titles or types changed, which no post-by-post skip should hide. An empty `_meeting_date` is a clerk's omission, not a format change, so it only skips that post: an undated meeting never fails the agency's check, even when it is the only meeting in the window. With recordings on, the feed is read the same way: a body that is not an Atom feed is `ErrMalformed`; a video with the title prefix but no valid id or publish time is skipped, and the check fails when every such video in the window (extended by `MaxDelay`) is unreadable; a fetch error fails the check like the posts' fetch does.

`www.goventura.org/robots.txt` allows everything except `/wp-admin/`.

Fixtures in `internal/sources/vctc/testdata`, recorded on 2026-11-18 and trimmed (post content without the site header, images, icons and all but one supporting document per heading):

- `vctc_commission_meetings.json`: the first 11 posts of the adapter's query: the four 2026 meetings with a meeting date (2026-07-10 to 2026-11-06, the October one with an amended agenda, September to November with presentations), the April 2026 post with a date, and six without one, including May and June 2026, which the agenda links date, and `Commission Meeting – December 2025 (Cancelled)`.
- `vctc_mixed_types_upcoming.json`: four posts of different types: `TTAC – November 2026` (post 25015), recorded at 12:43 local before its 13:30 start, the only upcoming post that day and so the shape of an upcoming meeting; the November commission meeting; a TRANSCOM meeting; and a cancelled TTAC meeting.
- `vctc_special_meetings.json`: four type-33 posts: a special meeting of the Commission, the goal-setting session, the Admin/Finance special meeting and the SPBL community meeting.
- `vctc_history.json`: all 208 posts of types 25 and 33 with their id, link, title, types and meta, and their content reduced to the main column's headings, every link under the agenda heading and the first link under each other heading, for the filter, date and evidence checks above.
- `vctc_youtube_feed.xml`: the channel feed with five of its fifteen entries: the November (titled November 11), October, September and July meeting videos and one other video. Recorded once, before the robots check above.

No commission meeting was upcoming on 2026-11-18, so `TestSourceIDsAreStableAcrossDailyRuns` derives the earlier states of post 24950 from the recorded one (agenda posted without presentations, agenda amended, the morning of the meeting, after its video) and adds later posts, feeds them in changing order, and checks that every `SourceID` keeps its meeting date and that the status moves from `scheduled` to `held`. Time, title, filter, agenda, date, evidence, recording and error cases use posts built in the tests. The integration tests run the production `vctc.VCTC` through `check_agency` (one request, to goventura.org only; the November meeting stored as `held` and its `process_agenda` job enqueued) and the YouTube configuration through two daily checks.

### Follow-up jobs

`check_agency` enqueues `process_agenda` when a meeting has an agenda URL, in the same transaction as the upsert; it also enqueues `download_audio` for a held meeting with a recording URL (see [Recording audio](#recording-audio)). The seam is `jobs.FollowUp`:

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

`check_agency` calls it for every upserted meeting, inside the upsert transaction; an error rolls back the agency's whole check. The default is `jobs.FollowUps{EnqueueProcessAgenda{Queue}, EnqueueDownloadAudio{Queue, Store, Enabled}}`, which runs each in order and stops at the first error. `EnqueueProcessAgenda` inserts `process_agenda` on the client's queue with `river.ClientFromContextSafely[pgx.Tx](ctx)` and `InsertTx`, and does nothing for a meeting without an agenda URL. `EnqueueDownloadAudio` is described under [Recording audio](#recording-audio). `jobs.NoFollowUp` does nothing. Another implementation is set through `jobs.Config.FollowUp`; one that also wants agendas processed or audio downloaded includes those follow-ups itself.

`process_agenda` has the same kind of seam for new agenda versions, `jobs.AgendaFollowUp`:

```go
type AgendaFollowUp interface {
	AgendaVersionAdded(ctx context.Context, tx pgx.Tx, v store.AgendaVersion) error
}
```

It is called once for each version that was created, never for an unchanged agenda, inside the transaction that inserted the version, so whatever it writes commits or rolls back with the version. It is for follow-up jobs: the summary job (CIV-39) is enqueued here, and the change story (CIV-40) will enqueue its job here too, with `InsertTx`. It must not record meeting events: `AddAgendaVersionTx` already records `agenda_published` or `agenda_changed` in the same transaction, so an event written here would be a duplicate. The default is `jobs.EnqueueSummarizeAgenda{Queue, Logger}` on the client's queue, which inserts `summarize_agenda` for the new version (see [Agenda summaries](#agenda-summaries)). `jobs.NoAgendaFollowUp` does nothing. Another implementation is set through `jobs.Config.AgendaFollowUp`; one that also wants summaries calls `EnqueueSummarizeAgenda` itself.

### Agenda summaries

`summarize_agenda` writes a plain-language summary on one agenda version, through `internal/llm` on the tier `AGENDA_SUMMARY_TIER` names (default `routine`). Its args are the agenda version id. The prompt lives in one place, `internal/summarize/agenda.go`, and its tests use `llmtest.Fake`. Two things enqueue it: the agenda follow-up hook, once for each new version, and the hourly sweep below, for versions still without a summary.

- **Flag.** `ingest-summarize-agendas`, separate from `ingest-check-agencies`, so agendas keep being stored while summaries are off, and summaries can be turned off alone (cost, a bad model).
- **Off or unconfigured costs nothing.** With the flag off, or with no LLM (the worker passes no client when `AI_GATEWAY_API_KEY` is empty; a client that answers `llm.ErrNotConfigured` is treated the same), the job completes at once without a model call or a failed attempt, and the sweep inserts nothing. Those versions stay without a summary until the sweep picks them up once the flag is on and the key is set.
- **Enqueue.** `EnqueueSummarizeAgenda` inserts the job in the transaction that adds the version, inside a savepoint. If the insert fails, even in the database, it rolls back to the savepoint, logs `agenda summary not enqueued` at `warn` and returns nil, so the version and its event still commit; the sweep enqueues it later. A summary can never block or roll back agenda ingestion: the job runs after the version committed, and none of its failures touch the version.
- **Keyed to one version.** The job reads the text of the version named in its args and writes the summary on that same row; versions are immutable, so a summary always describes the text it sits on.
- **Idempotent.** A version that already has a summary completes without a model call. The write is `store.SetAgendaSummaryIfUnset`, a conditional `UPDATE ... WHERE summary IS NULL`, so of two runs that race, the first write wins and the second leaves it alone. Two racing runs can both pay for a model call; they cannot both write.
- **Uniqueness.** The job is unique by args and queue over the non-final states only (`available`, `pending`, `retryable`, `running`, `scheduled`): while a job for a version is waiting, running or retrying, another insert is skipped, so jobs never stack. Once it is completed, cancelled or discarded, a new insert is accepted, which is what lets the sweep try again.
- **Superseded versions.** When a newer version of the meeting exists by the time the job runs, it completes without a model call. Only the latest version is shown.
- **Same text again.** When an earlier version of the same meeting has the same `content_hash` and a summary (an agenda that flips between the direct and the Firecrawl path and back), that summary is copied with no model call (`store.SummaryOfSameContent`).
- **No transaction across the call.** The job reads the version with plain queries, calls the model with no transaction open, and writes with one statement.
- **Untrusted text.** Agenda text comes from public websites and may contain instructions aimed at the model. The system prompt holds every instruction and says the document is data. The user message is a fixed line, then the document between a line `<document-D>` and a line `</document-D>`, where `D` is 32 hex characters from `crypto/rand`, new for every request and named in that request's system prompt. The job checks that the document does not contain `D`, in any case, and draws again if it does (8 tries, then `summarize.ErrNoDelimiter`, which cancels the job). The document is sent verbatim; nothing is stripped from it. The guarantee is that the document cannot contain its block's closing line, so it cannot end the block early or forge a block of its own; it is not a guarantee that the model obeys the boundary. Any other tag in the document, such as `</agenda>` or a guessed `</document-...>`, is text inside the block. Nothing in the document changes the tier, the system prompt (apart from `D`), the output limit or which row is written; the model's reply only ever becomes the summary text.
- **Plain-text output.** Every Unicode format character (category Cf: bidi overrides and isolates, zero-width joiners and spaces, word joiners, byte order marks, soft hyphens) and the whole tag block U+E0000 to U+E007F are removed from the reply. It is then normalized with `extract.Normalize` (control characters and NUL removed, whitespace collapsed) and capped at 4,000 bytes (`summarize.MaxSummaryBytes`) at a word, else at a UTF-8 character boundary, ending with `…`. This applies to the reply only. Agenda text keeps `extract.Normalize` unchanged, since a change there would change content hashes and create a new version of every stored agenda. Markup is not interpreted or stripped: the summary is plain text, and anything that shows it must escape it (React does) and never render it as HTML or Markdown.
- **Input limit.** The model gets at most the first 100,000 bytes of the agenda text (`summarize.MaxAgendaInputBytes`, roughly 25,000 tokens of English), cut at the last line break in the second half of that window, else at a UTF-8 character boundary. The user message then says the document was cut short, and the stored summary ends with the line `This summary covers only the first part of the agenda.` (`summarize.TruncatedNote`), inside the 4,000-byte cap. Agendas put the list of items first and attached staff reports after, so the head holds what the summary needs. There is no map-reduce over chunks: it would multiply the cost of every long agenda for detail the summary leaves out.
- **Timeout.** `LLM_TIMEOUT` plus one minute (3 minutes by default), so the gateway call's own limit fires first.
- **Logging.** Each attempt logs the tier, model, finish reason, input and output tokens, input bytes, text bytes and whether the input was cut; `agenda summary written` says whether the summary came from the model or was copied.

Errors, classified like `process_agenda`:

- Retryable, River retries with backoff: `llm.IsRetryable` (network failures, the `LLM_TIMEOUT` limit, the job's own timeout, 402, 408, 429, 5xx, an undecodable or empty reply, and a reply that is empty once cleaned), and database errors. A 429's `Retry-After` is not used for snoozing, as in the other jobs; River's backoff applies. After 5 attempts the job is discarded.
- Permanent, cancelled with `river.JobCancel`: other 4xx, `llm.ErrTruncated`, `llm.ErrContentFiltered`, `llm.ErrResponseTooLarge`, `summarize.ErrEmptyAgenda`, `summarize.ErrNoDelimiter`, and a version that no longer exists (`store.ErrNotFound`).

**The sweep.** `enqueue_agenda_summaries` is a River periodic job that runs every hour and when the worker starts, under the same flag. Its args carry the hour it belongs to and it is unique by args and queue, so a restart or a second leader inserts it once per hour. With the flag off or no LLM it does nothing. Otherwise it asks `store.ListUnsummarizedAgendaVersions` for at most 50 versions and inserts a `summarize_agenda` job for each with `InsertMany`; a version whose job is still waiting or running is skipped as a duplicate. A version qualifies when it is the latest version of its meeting, its summary is null, the meeting belongs to a live agency and starts inside the ingest window (30 days back to 90 ahead, as for `check_agency`), and no `summarize_agenda` job for it was cancelled or discarded in the last 7 days. Later meetings come first (`starts_at DESC`).

- It covers versions created before the job existed, versions created while the flag was off or the key missing, an enqueue that failed, and jobs that failed. There is no other backfill.
- **Backoff for a version that keeps failing.** A cancelled or discarded job leaves a row in `river_job`, and the client keeps those rows for 7 days (`CancelledJobRetentionPeriod` and `DiscardedJobRetentionPeriod`; River's default for cancelled jobs is 24 hours, and the setting applies to every job kind). The sweep skips a version while such a row exists, so a version that always fails is tried at most once a week, with at most 5 attempts each time, and only while its meeting is in the window. This needs no migration: the state is River's own table, matched on the job's `args->>'agenda_version_id'`.

**Cost.** One try of one version is at most 5 model calls, each with at most 100,000 bytes of document plus a fixed prompt in (roughly 25,000 tokens) and 1,024 tokens out. The hook tries each new version once, a copied summary costs nothing, and the sweep adds at most one try a week for a version that failed. Daily spend is bounded by the number of new agenda versions the daily check produces (a handful per agency) and, while a backlog exists, by the sweep's 50 versions an hour. There is no spend cap in the service: the only hard limit is the AI Gateway account's budget, whose exhaustion (402) is retried and then discarded, and turning off the flag stops all spend at once.

## Recording audio

`download_audio` downloads one recording and stores its audio track, never the video, in the [bucket](#object-storage). It is enqueued by `check_agency` and works through `internal/media`, which resolves the recording URL to a media stream or file and runs `ffmpeg` under resource limits on media that a gateway in the worker fetches from the allowed hosts; `ffmpeg` never fetches from the internet itself.

### Enqueueing

`jobs.EnqueueDownloadAudio`, one of the default [follow-ups](#follow-up-jobs), runs inside the check's upsert transaction. For a meeting that is `held` and has a recording URL, and only while `ingest-download-audio` is on, it upserts the recording (`store.UpsertRecordingTx`, unique on meeting and URL) and inserts `download_audio` with the recording id on the audio queue. It inserts nothing when:

- the recording already has audio;
- its audio was deleted by retention (`audio_deleted_at` set), so deleted audio is never downloaded again;
- an earlier download failed permanently, or failed and is backing off (see [Failures](#failures)).

A scheduled or cancelled meeting gets no recording row and no job. The recording is keyed by its URL: if a source changes the recording URL of a meeting, that is a second recording, downloaded and stored again; the first keeps its audio until retention deletes it.

`download_audio` is unique by args, queue and UTC day, like `process_agenda`. It has at most 5 attempts and a timeout of `AUDIO_TIMEOUT` plus 15 minutes for resolving the player page and uploading. It runs on its own queue, `audio` (`jobs.Config.AudioQueue`), with one worker, so the worker never runs two downloads at once: memory and disk use are those of one `ffmpeg`.

River's stuck-job rescuer keeps its default of one hour for every job. In River 0.51 the rescuer leaves a running job alone until the job's own `Timeout()` has passed as well, so a download (75 minutes with the defaults) is not rescued at one hour, and an interrupted `check_agency` or `process_agenda` is still rescued after one hour. `TestRescuerLeavesARunningDownloadAloneWhileRescuingOtherJobs` checks both. A download cut off by a deploy is rescued once its 75 minutes have passed.

### What the job does

1. With `ingest-download-audio` off, it completes without doing anything. Without a downloader (`jobs.Config.Audio`) it is cancelled (`jobs.ErrNoDownloader`). Without a bucket (`jobs.Config.Storage`), the default is an unconfigured bucket, which cancels the job at step 3.
2. It reads the recording. A missing recording cancels the job. A recording that already has audio, whose audio was deleted by retention, or that is still backing off after a failure, completes without downloading.
3. It asks the bucket for the key `recordings/{recording id}.mp3` (`jobs.AudioKey`). If an object is there, an earlier run uploaded it and failed before recording it, so the job records it without downloading again. A bucket that is not configured cancels the job here, before any download.
4. It downloads the audio into a new temporary directory named `civic-audio-*` (removed on every return path) with `jobs.Config.Audio`, a `jobs.AudioDownloader`; the worker passes `media.NewDownloader(cfg.Audio, FETCH_USER_AGENT)`. At startup the worker removes `civic-audio-*` directories in the temporary directory older than `AUDIO_TIMEOUT` plus an hour, left by a worker that was killed mid-download.
5. It streams the file to the bucket with `storage.Store.Put`.
6. In one transaction it records the key and an expiry six months from now with `store.AddRecordingAudioTx`, which writes only while the recording has no audio and none was deleted and clears any recorded failure, and calls the audio follow-up hook, `jobs.AudioFollowUp`, with the updated recording. If another run stored the audio meanwhile, the job completes. If retention deleted it meanwhile, the job deletes its own upload again and completes.

Running the job twice for one recording downloads once: the second run finds the audio recorded (step 2) or the object in the bucket (step 3). The key depends only on the recording id, so a repeated upload replaces the same object.

`AudioFollowUp` is the seam for transcription (CIV-41): `AudioStored(ctx, tx, recording)` runs in the transaction that records the audio, so a job it inserts with `InsertTx` commits or rolls back with it. It is called once per recording, never for audio that was already stored. The default, `jobs.NoAudioFollowUp`, does nothing. Retention (CIV-42) deletes the object and calls `store.MarkRecordingAudioDeleted`; the job never downloads such a recording again.

### Failures

Migration `00010` adds the download state to `recordings`: `download_attempts` (failures recorded so far), `download_failure` (the kind of the last one), `download_failed_at` and `download_retry_after`. `media.FailureKind` names the kind of an error, and the job records it with `store.RecordDownloadFailure` when the job is cancelled, or when a retryable error happens on the last of its 5 attempts. A successful download clears the kind and the times and keeps the count.

| Kind | Errors | Next try |
| --- | --- | --- |
| `invalid_url` | a recording URL that is not `http` or `https` with a host (`media.ErrInvalidURL`) | never |
| `unsupported` | a recording URL on no known host (`media.ErrUnsupported`), a redirect or playlist entry off the allowed hosts or ports (`media.ErrForbiddenHost`), a private or loopback address (`media.ErrForbiddenAddress`), encrypted HLS | weekly |
| `no_media` | a player page that names no media, an empty playlist | daily for the first 3 days after the meeting's start, weekly after |
| `no_audio` | media without an audio track, output under 10 seconds | weekly |
| `malformed` | media `ffmpeg` cannot read (`Invalid data found`, `moov atom not found`), a crash, a broken or self-referencing playlist, a segment listed twice, audio longer than the recording beyond the tolerance | weekly |
| `refused` | a 4xx from the player page or a media host (`media.ErrRefused`), including CloudFront's 403 | weekly |
| `limit` | longer than `AUDIO_MAX_DURATION`, larger than `AUDIO_MAX_BYTES`, over the time or memory limit, too many playlist entries or requests | weekly |
| `incomplete` | a skipped segment with no HTTP status, a result shorter than the recording, a live playlist | daily; weekly when the previous failure was also `incomplete` |
| `unavailable` | `limit`, `ffmpeg`, `ffprobe` or `yt-dlp` missing | daily |
| `transient` | network errors, 408, 429, 5xx, bot checks, certificate and TLS errors, and any error the job does not recognise (after the 5th attempt) | daily |

"Never" is kept only for `invalid_url`: the recording row is keyed by its URL, so a URL that is not `http(s)` can never become valid; a corrected URL is a new recording. Every other kind can change on the source's side or ours (a clip encoded late, a host added to `media.DefaultHosts`, an access decision, a fixed parser), so it is tried again. Retries continue only while the meeting is inside the daily check's 30-day look-back, since the check enqueues nothing for meetings outside it. "Weekly" sets `download_retry_after` to 6.5 days after the failure and "daily" to 12 hours, so the next daily check after that time enqueues it again; the half day keeps a check that runs a few minutes earlier than the day before from skipping a whole extra day. `no_media` is daily at first because a Granicus clip is often listed before its video is encoded. `refused` stays on the weekly schedule rather than being permanent, so a later access decision about the Granicus media hosts takes effect without clearing rows by hand. To retry earlier by hand, set `download_failure` and `download_retry_after` to null.

Errors that River retries within one job: `media.IsRetryable` (network failures and 408, 429 and 5xx answers from the player page or media host, a failed segment, a bot check on the player page, an unrecognised error, a canceled context or the job's own timeout), retryable bucket errors, and database and follow-up errors. `incomplete` is not retried within the job: a short result usually repeats, so it is recorded at once and tried the next day. Bucket errors that are not retryable (not configured, access denied) cancel the job and are not recorded on the recording, since they are not about the recording.

### Which hosts are fetched

`media.DefaultHosts` lists them; a new source whose recordings live elsewhere adds its hosts there.

| Kind | Hosts | Fetched by |
| --- | --- | --- |
| Player pages | `*.granicus.com` (`MediaPlayer.php?view_id=&clip_id=`, which redirects to `/player/clip/{id}`) | `media.PageFetcher`, with `FETCH_USER_AGENT`, at most 4 MiB, no Firecrawl |
| Media and playlists | `archive-stream.granicus.com` (HLS), `archive-video.granicus.com` (MP4), `*.googlevideo.com` (YouTube audio) | `media.PageFetcher` for playlists, then `media.Gateway` for segments and files, with `FETCH_USER_AGENT` |
| YouTube pages | `youtube.com`, `www.youtube.com`, `m.youtube.com`, `youtu.be` | `yt-dlp` |

- **Granicus** (LA Metro's and OCTA's Legistar `EventMedia`, Metrolink's video feed). The adapters give the `MediaPlayer.php` link. The job fetches that page and reads the media URL from it: the `.m3u8` HLS stream on `archive-stream.granicus.com` when there is one, otherwise the first `.mp4` on a media host. The page writes the URL plainly (`video_url="..."`) and JSON-escaped (`"https:\/\/..."`); both are read. The media file's uuid is read from the page, never derived from the clip id or the RSS `guid`. An RSS enclosure only shows that a clip exists, so a page that names no media is `no_media` and nothing is stored. A bot check on the page (the interstitial markers of `internal/extract`) is retried. A page that redirects off `*.granicus.com` is refused.
- **Direct media.** A recording URL on a media host whose path ends in a media extension (`.m3u8`, `.mp4`, `.m4a`, `.mp3`, `.webm` and similar) is used as is. A recording URL on any host not listed is `unsupported`, cancelled with a log line naming the host.
- **YouTube** (VCTC, whose adapter gives `https://www.youtube.com/watch?v=` links). `yt-dlp --use-extractors youtube --get-url -f bestaudio` resolves the page to a `googlevideo.com` audio URL, which the gateway then fetches like any media file. `yt-dlp` runs under the same limits, with a 2-minute timeout, `--ignore-config`, `--no-cache-dir` and at most 64 KiB of output, and uses the image's `deno` for YouTube's JavaScript challenges. `--use-extractors youtube` keeps it from falling back to its generic extractor and fetching whatever page it is given. It is not behind the gateway: it talks to YouTube with its own browser-like identity, not `FETCH_USER_AGENT`, since YouTube is not one of the CIV-62 hosts. Not tried against live YouTube.

Following CIV-62, the Granicus hosts, whose `robots.txt` disallows all crawlers, are fetched with our identifying User-Agent. A recording's player page and media are fetched once per download attempt: once for a recording that succeeds, and again on each retry or after each back-off period for one that fails (see [Failures](#failures)). Firecrawl is not used here: its markdown drops the player's script, and it cannot fetch media.

**CloudFront.** On 2026-11-18, `archive-stream.granicus.com` and `archive-video.granicus.com` answered `403` from CloudFront to requests with `FETCH_USER_AGENT`, both for Metrolink clip 1438's HLS playlist and its MP4 (and for `robots.txt`), while `metrolink.granicus.com` served the player page. CIV-13 found that CloudFront blocks curl's default User-Agent; our identifying one is blocked as well. Until that is settled with Granicus or the agencies, Granicus downloads end as `refused` and are retried weekly. The job does not imitate a browser.

### Network guard

Recording URLs come from agency pages, and the media they name comes from third parties, so every request the job makes for media is limited to the hosts above, to their standard ports and to public addresses, and `ffmpeg` itself never fetches anything from the internet.

- **Addresses.** `media.Guard` dials through a `net.Dialer` whose `Control` hook checks the address actually being connected to, after DNS resolution. Refused (`media.ErrForbiddenAddress`): loopback, private (RFC 1918 and `fc00::/7`), link-local (including `169.254.169.254`), unspecified and multicast addresses, `0.0.0.0/8`, shared address space `100.64.0.0/10`, benchmarking `198.18.0.0/15`, reserved `240.0.0.0/4` (including broadcast), IPv4-mapped and IPv4-compatible IPv6 (`::/96`), NAT64 (`64:ff9b::/96`, `64:ff9b:1::/48`) and 6to4 (`2002::/16`). Checking at connect time covers DNS names that resolve to such an address and redirects to one.
- **Hosts and ports.** Every URL, including every redirect target, must be `http` on port 80 or `https` on port 443 to a listed host; anything else is `media.ErrForbiddenHost`.
- **Player pages and playlists.** `media.PageFetcher` is its own HTTP client, not `internal/fetch`: the guarded dialer, Go's certificate verification, no proxy from the environment, at most 5 redirects, each checked before it is followed. The agenda and adapter fetchers in `internal/fetch` are unchanged.
- **Playlists.** `media.InspectPlaylist` fetches the HLS playlist with `PageFetcher` and checks it: it must start with `#EXTM3U`; every variant, audio rendition, segment, key, init section (`EXT-X-MAP`) and I-frame playlist URI must resolve to an allowed URL; keys must be `METHOD=NONE` (encrypted HLS is `unsupported`); byte-range playlists (`EXT-X-BYTERANGE`, or a `BYTERANGE` on `EXT-X-MAP`) are `unsupported`, refused before anything is downloaded, rather than fetching the whole file once per part; a segment URI may appear only once; none may refer to the playlist itself. From a master it builds an ordered list of candidates, without duplicates (one audio rendition shared by several groups is one candidate): audio renditions (`EXT-X-MEDIA:TYPE=AUDIO` with a `URI`) first, then variants by what `CODECS` says, audio-only variants first, then variants with audio and video, then variants without a `CODECS` attribute (which may carry audio), each group by ascending `BANDWIDTH` (a missing bandwidth last). Variants whose `CODECS` name no audio codec (`mp4a`, `ac-3`, `ec-3`, `ac-4`, `opus`, `flac`, `alac`, `mp3`, `vorbis`, `dts*`) are left out, and I-frame playlists are never candidates. At most 16 candidates; a master with none that may carry audio is `no_audio`. The job tries the first candidate, fetching only its media playlist (a nested master is `malformed`); if that yields no audio track (`no_audio`), it tries the next, up to 4 within the same job. A media playlist must end with `EXT-X-ENDLIST` (a live or unfinished stream is `incomplete`), have at least one segment, and have at most 20,000 entries. The origin playlist is fetched once: what is downloaded is the list of parts read here, so a host that serves a different playlist later changes nothing.
- **Media gateway.** `media.Gateway` fetches every media byte on `ffmpeg`'s behalf, over Go's `net/http` with the guarded dialer and full certificate verification against the system roots (tests inject their own CA). It sends `FETCH_USER_AGENT`, passes on only `Range`, follows redirects itself (checking each `Location` before connecting), and refuses a response whose first bytes are `#EXTM3U`, so a segment or file cannot smuggle in a playlist. It counts requests: a playlist allows three attempts per part plus 10, a file 2,000 (probe included); past that the run is a `limit` failure. Without a bound, `ffmpeg` was measured making 110,057 requests in 15 seconds for one self-referencing master playlist.
  - **HLS.** The media playlist is split into runs: a new run starts at every `EXT-X-DISCONTINUITY` and wherever the init section (`EXT-X-MAP`) changes, and every run of a fragmented-MP4 stream begins with its init section. For each run the job streams the parts through `Gateway.Copy` into the standard input of its own `ffmpeg`, so each `ffmpeg` reads one continuous MPEG-TS or fragmented MP4 stream from `pipe:0` and never sees a URL. A recording restarted after a recess, with new PIDs or timestamps that jump, is therefore read as separate streams instead of one stream whose second program the demuxer drops. A part that fails with a network error, 408, 429 or 5xx before any of its bytes were sent is tried up to 3 times; a 4xx is `refused`, a later failure `transient`, and in both cases the run fails rather than skip the part. A part that cannot be fetched therefore never leaves a gap.
  - **Files** (an MP4 or a `googlevideo.com` URL, which `ffmpeg` must be able to seek in). The gateway also serves HTTP on `127.0.0.1`, started for each run: the file's URL is registered and `ffmpeg` and `ffprobe` are given only the gateway URL (`http://127.0.0.1:PORT/r/0/name`). Only `GET` and `HEAD` of that path are served; anything else, and every refusal, answers 403 and fails the run as `unsupported` whatever `ffmpeg` made of it. Only the status and the `Content-Type`, `Content-Length`, `Content-Range`, `Accept-Ranges`, `Last-Modified` and `ETag` headers are passed back.
  - **Streaming.** Bodies are copied in 32 KiB pieces, never buffered whole. A transfer stays open as long as data moves in either direction: the upstream read deadline is renewed before every read and the downstream write deadline before every write, and a transfer that makes no progress for 60 seconds (`media.DefaultIdleTimeout`) is dropped. For files, `ffmpeg`'s `-rw_timeout` is 30 seconds longer, so the gateway gives up first.
- **What `ffmpeg` may open.** For HLS `-protocol_whitelist pipe`: standard input and nothing else. For files `-protocol_whitelist http,tcp`: `http` to reach the gateway, and `tcp` only because `http` runs on top of it. Neither list has `https`, `tls`, `httpproxy` (whose own direct connection would bypass any proxy), `crypto`, `file`, `data`, `concat`, `subfile`, `udp` or any other protocol. Both runs use `-format_whitelist mpegts,mov,mp4,m4a,3gp,3g2,mj2,mp3,aac,matroska,webm,ogg,wav,flv`, which leaves out `hls` and `concat`, the demuxers that open URLs named in the data they read; `mov` reference tracks stay off by default. So `ffmpeg` opens exactly one input, standard input or the gateway URL, and nothing named inside the media. Its environment has `http_proxy`, `https_proxy` and `all_proxy` empty and `no_proxy=*`, so an environment proxy setting cannot route it elsewhere. `TestFFmpegCannotReachTheNetworkExceptThroughTheGateway` gives `ffmpeg` hostile playlists, both on standard input and as a fetched file, whose segment, variant or key is an `httpproxy://`, `https://`, `tcp://`, `tls://`, `crypto+http://`, `concat:`, `subfile,`, `data:`, `file:` or `udp://` URL (and a plain `http://` segment) aimed at a listening socket, and checks that nothing connects to it and no audio comes out. The same test passes against Debian's `ffmpeg` 7.1 in the worker image, where an `http`-based whitelist with the `hls` demuxer enabled did open a `tcp://` variant playlist directly.
- **What is not covered.** `yt-dlp` is not behind the gateway (above): it talks to YouTube directly, and only the `googlevideo.com` URL it returns goes through the gateway. A media host on the list that is itself compromised can still serve arbitrary media, which the limits below bound.

### Audio extraction

`media.Tool.ExtractAudio` first works out how long the audio should be: for HLS the `EXTINF` total of the chosen media playlist (the audio rendition's when there is one), otherwise with `ffprobe` through the gateway (`-select_streams a:0 -show_entries stream=duration:format=duration`: the audio stream's own duration when the container gives one, else the container's). A recording longer than `AUDIO_MAX_DURATION` fails as `limit` before anything is downloaded. Then it runs:

```text
limit -max-memory AUDIO_MAX_MEMORY -max-file-bytes AUDIO_MAX_BYTES -- \
  ffmpeg -nostdin -loglevel warning INPUT \
    -map 0:a:0 -vn -sn -dn -t AUDIO_MAX_DURATION -ac 1 -ar 16000 -c:a libmp3lame -b:a 32k \
    -write_xing 0 -id3v2_version 0 -f mp3 OUT

INPUT for HLS:   -protocol_whitelist pipe -format_whitelist mpegts,mov,...,flv -i pipe:0
INPUT for files: -protocol_whitelist http,tcp -format_whitelist mpegts,mov,...,flv -rw_timeout 90s \
                 -i http://127.0.0.1:PORT/r/0/NAME
```

- **Audio only.** `ffmpeg` writes only the first audio track, re-encoded as 16 kHz mono 32 kbps MP3, about 14 MB per hour. That is the format `internal/transcribe` cuts its chunks to. The video is demuxed and dropped, never decoded or written; where the playlist has an audio-only rendition, the video is not even downloaded. Nothing is held in memory; the only file on disk is the MP3.
- **Complete or nothing.** For HLS the job fetches every segment itself and fails the run if any cannot be fetched (above). It still runs `ffmpeg` at `-loglevel warning` and fails on a `Failed to open segment` or `failed too many times, skipping` line, in case a future input path lets `ffmpeg` fetch segments. After a clean run it measures the MP3 with `ffprobe` and compares it with the expected length:
  - Against an audio stream's own duration, the MP3 may be up to 2 seconds plus 0.1 % shorter. Re-encoding changes the length only by encoder priming and padding, well under a second.
  - Against a container or playlist length, it may be up to 3 seconds plus 0.5 % shorter (18 seconds an hour, 54 seconds for a three-hour meeting). Recordings often start the audio a second or two after the video, `EXTINF` values are rounded per segment, and the container length is that of the longest stream. A lost segment fails the fetch, not this check; the length check is for a stream that ends early, which loses far more than this.
  - **Runs.** Each HLS run is written to its own file in a temporary directory next to the output and measured with `ffprobe`. The limits apply to the whole job, not to each run: all runs share one `AUDIO_TIMEOUT` deadline, each run gets `-t` set to what is left of `AUDIO_MAX_DURATION` and an `RLIMIT_FSIZE` of what is left of `AUDIO_MAX_BYTES`, and the gateway's request budget covers every run. The run files are then joined by plain byte concatenation into the output, which fails as `limit` past `AUDIO_MAX_BYTES`, and the temporary directory is removed on every return path. Byte concatenation is valid because every file is written without an ID3 tag and without a Xing/LAME header (`-write_xing 0 -id3v2_version 0`): each is a bare sequence of MP3 frames with the same parameters (MPEG-1 Layer III is not used at 16 kHz; these are MPEG-2 Layer III frames, 16 kHz mono, constant 32 kbps), and a sequence of such frames is itself a valid MP3 stream whose length `ffprobe` computes from size and bitrate. A Xing header would carry the first run's frame count and make readers report only the first run's length, which is why it is left out everywhere. Each join adds at most one encoder delay (about 0.07 s) of silence. `TestExtractAudioHandlesHLSStreamShapes` checks a joined file with `internal/transcribe`'s own `FFmpeg.Duration` and cuts a chunk across the join with its `FFmpeg.Extract`.
  - The total length of the runs is what is compared with the expected length below. A result longer than expected by more than the same tolerance is `malformed` (a source whose parts overlap or repeat).
  - Shorter than that is `incomplete`. It is not retried within the job, since it usually repeats; it is tried again the next day, and weekly once it has failed as `incomplete` twice in a row, so a recording that is genuinely shorter than its container says costs one download a week.
  - A recording whose length is unknown and whose audio reaches `AUDIO_MAX_DURATION` is `limit`: it was cut off by `-t`, and a truncated file is not stored as if it were complete.
  - In every failure the partial MP3 is removed.
- **Never a page as audio.** What is stored is always the encoder's output, and the job checks that it starts with an MP3 frame header (or an ID3 tag, for files written elsewhere) and is at least 40,000 bytes (about 10 seconds; `media.DefaultMinBytes`). An HTML page, an error page or a bot check at a media URL fails in `ffprobe` or `ffmpeg` (`Invalid data found`), and media with no audio track fails the `-map`, so neither is stored. `-format_whitelist` limits the demuxers that ever see untrusted input.
- **Bounded.** The child is killed (its whole process group) when `AUDIO_TIMEOUT` passes or the job's context ends, and a transfer that makes no progress for 60 seconds is dropped by the gateway. `cmd/limit`, a small binary shipped as `/app/limit`, sets `RLIMIT_DATA` (on Linux; macOS does not allow it, so locally only the other limits apply) and `RLIMIT_FSIZE` on itself and then `exec`s the command, which inherits them: `ffmpeg` cannot map more than `AUDIO_MAX_MEMORY` of writable memory, and is killed by `SIGXFSZ` once its output passes `AUDIO_MAX_BYTES`. `MALLOC_ARENA_MAX=2` keeps glibc from reserving an arena per thread. A memory failure in the child never reaches the worker's own process. `ffprobe` and `yt-dlp` run under the same limits.
- **Errors.** Killed for time, size or memory (`SIGKILL`, `SIGXFSZ`, `Cannot allocate memory`) is `media.ErrLimitExceeded`. Otherwise `ffmpeg`'s message decides: `Server returned 5xx`, 408, 429, timeouts, refused or reset connections are `media.ErrNetwork` (retried); `Server returned 4xx` is `media.ErrRefused`; `matches no streams` and similar are `media.ErrNoAudio`; `Invalid data found when processing input`, `moov atom not found`, `could not find codec parameters` and a crash are `media.ErrMalformed`; anything else is `media.ErrUnrecognized`, retried and recorded as `transient`. A missing `limit`, `ffmpeg`, `ffprobe` or `yt-dlp` is `media.ErrUnavailable`.
- **Finding the tools.** `AUDIO_LIMIT_BINARY`, or `limit` next to the running executable, as for `cmd/extract`; `make run-worker` builds it into `api/bin`. The worker logs the paths at startup and warns when `limit` is missing, and warns when the bucket is not configured (with whether `ingest-download-audio` is on at that moment), since every download would then be cancelled. `ffmpeg`, `ffprobe` and `yt-dlp` are looked up on `PATH` unless set.

| Variable | Default | Meaning |
| --- | --- | --- |
| `AUDIO_LIMIT_BINARY` | `limit` next to the worker | Path to the `cmd/limit` binary. |
| `AUDIO_FFMPEG_BINARY` | `ffmpeg` | `ffmpeg` to run. |
| `AUDIO_FFPROBE_BINARY` | `ffprobe` | `ffprobe` to run. |
| `AUDIO_YTDLP_BINARY` | `yt-dlp` | `yt-dlp` to run. |
| `AUDIO_TIMEOUT` | `60m` | Wall-clock limit on one extraction. The job's timeout is this plus 15 minutes. Must be positive. |
| `AUDIO_MAX_DURATION` | `12h` | Longest recording downloaded. Must be positive. |
| `AUDIO_MAX_MEMORY` | `1073741824` | `RLIMIT_DATA` of the child in bytes (1 GiB). Must be positive. |
| `AUDIO_MAX_BYTES` | `268435456` | Largest audio file in bytes (256 MiB, about 18 hours at 32 kbps). Must be positive. |

### The worker image

The worker has its own image, `api/Dockerfile.worker`; the API keeps `api/Dockerfile`, distroless and unchanged. The worker's final stage is `debian:trixie-slim`, because `ffmpeg` and `yt-dlp` need a C library and Python. It adds Debian's `ffmpeg` (7.1) and `python3` packages, the `yt-dlp` release `2026.10.26` (the platform-independent zipapp, downloaded from the GitHub release in its own build stage and checked against its SHA-256), and `deno` 2.9.8 copied from the official `denoland/deno:bin-2.9.8` image, and builds only `cmd/worker`, `cmd/migrate`, `cmd/extract` and `cmd/limit` into `/app`. It runs as `nonroot` (uid and gid 65532), and its entrypoint is `/app/worker` in exec form, so the worker is PID 1 and receives Railway's SIGTERM itself.

- The `debian:trixie-slim` and `denoland/deno` base images are pinned by digest, with the tag kept for reading. The Go build stage uses the same `golang` tag as `api/Dockerfile`, as before, so one Go upgrade changes both. Dependabot does not watch Dockerfiles; bump the digests by hand.
- The apt packages, `ffmpeg` among them, are not pinned: each build installs the current Debian trixie version (7.1.5 on 2026-11-18). Debian stable only takes security and bug fixes, so the version moves within 7.1.
- Bump `YTDLP_VERSION` with its `YTDLP_SHA256` together; YouTube changes often break old `yt-dlp` releases.
- Go as PID 1 does not reap orphaned grandchildren. `ffmpeg`, `ffprobe` and `yt-dlp` are waited for, and a timeout kills their whole process group, so none is left running; a process that `yt-dlp` itself leaves behind when it exits normally would stay a zombie until the container restarts.

### Tests

The job's integration tests use a fake downloader and the in-memory bucket: a held meeting gets one job and one stored object, a repeat run downloads nothing, audio deleted by retention is never enqueued or downloaded, the flag off adds no recordings, each failure kind is retried, recorded and backed off as in the table above (including a refused recording that downloads once access comes back), an object left by an earlier run is recorded without a download, and the two races (another run stores the audio; retention deletes it) end as described. One test runs the whole path: `check_agency` with a Granicus-style player page, a generated HLS stream served by `httptest`, the real `cmd/limit`, `ffmpeg` and `ffprobe`, the gateway, and the S3Mock bucket.

`internal/media` tests the resolver against a trimmed recording of Metrolink's player page for clip 1438 (`testdata/metrolink_player_1438.html`, 2026-11-18); the playlist checks, runs, byte-range refusal and the ordering of candidates; the guard's address, host and port rules; the gateway (registered paths only, `Range`, its User-Agent, redirects to other hosts and schemes, a playlist served as a segment, the request budget, streaming parts with their failures classified, a slow transfer longer than the idle timeout that still completes and a stalled one that is dropped, certificate verification against an injected CA); the tool's error classification with shell-script stand-ins for `ffmpeg`, `ffprobe` and `yt-dlp`; and real `ffmpeg` on generated media: a healthy MP4, HLS and master playlist; a missing middle segment, a missing last segment and a failing segment; a playlist that claims more than its segments hold; a live, self-referencing and nested playlist; a single-file byte-range playlist (refused after one request); a master whose lowest variant is video-only, with and without `CODECS` (the job falls back to the next candidate); one audio rendition listed by two groups; a discontinuity whose second part has different PIDs and timestamps offset by 500 seconds (20 s stored from 12 s + 8 s, checked with the transcription client's duration and chunk extraction); runs sharing the size limit; a playlist swapped after inspection; redirects and segments to other hosts and schemes; an allowed name on a loopback address; a non-default port; `https` upstream, verified in Go whatever TLS library `ffmpeg` was built with; and the protocol test above. Tests that need `ffmpeg` skip when it is not installed; the `api-integration` CI job installs it. The `media` and `jobs` suites have also been run inside the worker image, with the Go toolchain copied in, against its Debian `ffmpeg` 7.1.

## Agendas

A meeting's agenda is kept as numbered versions in `agenda_versions` (migration `00004`). Each row belongs to one meeting and holds the source document URL, the extracted text, a content hash, a nullable AI summary, a nullable change note, and `created_at`/`updated_at`. `version` starts at 1 for each meeting and is unique per meeting. The latest version, the highest number, is the one shown. Versions are never deleted or rewritten; only the summary and the change note are set after the insert.

`internal/store/agendas.go`:

| Function | Does |
| --- | --- |
| `AddAgendaVersion(ctx, in)`, `AddAgendaVersionTx(ctx, tx, in)` | Adds a version from `AgendaVersionInput{MeetingID, SourceURL, Text, ContentHash}`. When the meeting's latest version has the same hash, it adds nothing and returns that version with `created` false; otherwise it inserts the next number, records an `agenda_published` (version 1) or `agenda_changed` (later versions) meeting event in the same transaction (see "Meeting events"), and returns `created` true. A missing meeting is `ErrNotFound`. |
| `LatestAgendaVersion(ctx, meetingID)` | The highest-numbered version, or `ErrNotFound` when the meeting has none. |
| `ListAgendaVersions(ctx, meetingID)` | Every version, newest first. Empty for a meeting with none. |
| `GetAgendaVersion(ctx, id)` | One version by id, with its text. A missing version is `ErrNotFound`. |
| `SetAgendaSummary`, `SetAgendaChangeNote` (and `...Tx`) | Sets the field on one version by id and returns the updated version. A missing version is `ErrNotFound`. |
| `SetAgendaSummaryIfUnset(ctx, id, summary)` | Sets the summary only when the version has none, in one conditional `UPDATE`. Returns the version and whether this call wrote it; when it did not, the version carries the summary already there. A missing version is `ErrNotFound`. |
| `SummaryOfSameContent(ctx, v)` | The summary of the latest other version of the same meeting with the same content hash, when one has a summary. |
| `ListUnsummarizedAgendaVersions(ctx, params)` | Ids of the latest versions without a summary, for meetings of live agencies starting in `[From, To]`, skipping versions with a job of kind `FailedKind` cancelled or discarded within `FailedWithin`, later meetings first, at most `Limit`. It reads River's `river_job` table. |

The `...Tx` variants run in the caller's transaction, so a job can add a version and enqueue its follow-up River job atomically.

**Content hash.** The caller chooses what is hashed; the store chooses how. `store.ContentHash` can only be made with `store.HashContent(s)`, which returns the SHA-256 of `s` as lowercase hex, so a caller cannot pass an arbitrary string, a different algorithm or a hash of the wrong thing by accident. Its zero value is rejected with `ErrZeroContentHash` before the database is touched, and the column's check constraint accepts only 64 lowercase hex characters. Hash the text in the form that decides whether the agenda changed (for example, the normalized text, so a whitespace-only change is not a new version) and store the extracted text as `Text`. Compare hashes with `==`; `String()` gives the hex.

**Concurrency.** Two jobs may add a version for the same meeting at once. `AddAgendaVersion` locks the meeting row with `SELECT ... FOR NO KEY UPDATE` before it reads the latest version, then inserts. Adds for one meeting run one at a time; adds for different meetings never wait for each other. `FOR NO KEY UPDATE` does not block inserts elsewhere that only reference the meeting by foreign key. The lock is held until the transaction ends, which for the `...Tx` variants is when the caller commits or rolls back, so keep that transaction short. It also waits on a concurrent `UpsertMeetingTx` of the same meeting. The `...Tx` variants need the transaction at `READ COMMITTED`, Postgres's default and what `pool.Begin` gives: under `REPEATABLE READ` the waiting transaction would not see the version committed ahead of it, and the unique constraint on `(meeting_id, version)` would reject its insert instead of silently duplicating a number. `TestAddAgendaVersionConcurrentAddsGetDistinctNumbers` runs concurrent adds, through both variants, and checks the numbers are 1 to N with none lost; without the lock it fails with unique violations.

The migration's down half drops the table. `api/migrations/down_integration_test.go` applies every migration in a throwaway schema, rolls back to `00003`, checks the table is gone, and applies it again, without touching the shared tables other tests use.

### Agenda text

`internal/extract` turns a downloaded agenda into text. `extract.Extractor.Text(ctx, extract.Request{ContentType, Body, ExpectPDF})` returns the normalized text, and `extract.Normalize(s)` applies the same normalization to any string. The worker builds it with `extract.NewSubprocess(cfg.Extract)` as its PDF reader.

- **Format.** A body with `%PDF-` in its first 1024 bytes is a PDF whatever its content type, since agency servers often send PDFs as `application/octet-stream`. Otherwise the media type decides: `application/pdf`; `text/html` and `application/xhtml+xml`; `text/plain` and `text/markdown`, taken as text (Firecrawl answers in markdown). A missing or `application/octet-stream` type is sniffed with `http.DetectContentType`. Anything else, such as a Word document or an image, is `extract.ErrUnsupported`.
- **Bot checks and error pages.** Some hosts answer `200 text/html` with a bot check instead of the document; Imperva's "Pardon Our Interruption" page, served for a `.pdf` URL, extracts to about 600 characters of plausible English. So HTML is `extract.ErrBlocked` when the request expects a PDF, and HTML or text is `extract.ErrBlocked` when its first 256 KiB contain a known marker of Imperva/Incapsula, Cloudflare, Akamai, Sucuri or an F5 rejection page (the list is in `internal/extract/blocked.go`; `testdata/imperva_interstitial.html` is a recorded-style example). Nothing blocked is ever stored.
- **Plausibility floor.** Text with fewer than 200 non-space characters (`extract.DefaultMinChars`; `Extractor.MinChars` overrides it) is `extract.ErrNoText`. A real agenda, even a cancellation notice with its header, is longer; an empty page, a placeholder or an error message is not.
- **HTML and text.** Decoded by the charset in the content type, a byte order mark or, for HTML, the page's own `meta` (`golang.org/x/net/html/charset`); text without a declared charset that is not valid UTF-8 is read as Windows-1252, as browsers do. HTML is parsed with `golang.org/x/net/html`. Text in `head`, `script`, `style`, `noscript`, `template`, `svg`, `iframe` and `object` is dropped. Block elements (paragraphs, headings, list items, table rows, `br` and the like) start new lines; table cells in a row are joined with a space. A page nested more than 512 elements deep is `extract.ErrMalformed`.
- **Normalization.** Invalid UTF-8, U+FFFD replacement characters (fonts without a usable Unicode map produce them), NUL and other control characters, byte order marks, soft hyphens and zero-width spaces are removed (Postgres `text` rejects NUL and invalid UTF-8). Any run of spaces, tabs or Unicode spaces becomes one space, each line is trimmed, and empty lines are dropped. Line breaks are `\n`.

The normalized text is both what is stored as the version's `Text` and what is hashed, so a change only in whitespace, line endings or blank lines is not a new version. The same agenda read once directly and once through Firecrawl's markdown gives different text, so a switch between the two paths stores a new version (and records `agenda_changed`); telling real changes from such noise is CIV-40.

#### PDF extraction in a child process

PDFs are read in pure Go with [`github.com/ledongthuc/pdf`](https://github.com/ledongthuc/pdf), so the image needs no `pdftotext`. That library can loop forever or allocate without bound on a hostile or broken file: a page tree that lists itself in `/Kids`, a `/Count` of two billion, or a deflate bomb of under a megabyte that expands to gigabytes of glyphs. It does this inside its own code, where `recover` and a context cannot reach. So the worker never parses a PDF in its own process.

- **The child.** `cmd/extract` is a separate binary. It reads one PDF on stdin, writes the text on stdout and exits 0, or writes the reason on stderr and exits 10 (malformed), 11 (a limit) or 12 (too little text). The worker image (`api/Dockerfile.worker`) builds it into `/app`, so it ships as `/app/extract` next to `/app/worker`; `api/Dockerfile` builds every `./cmd/...` binary as before.
- **The parent.** `extract.Subprocess` runs it for each PDF with `exec.CommandContext`. It kills the child when `EXTRACT_PDF_TIMEOUT` passes or when the job's context ends, and reads at most `EXTRACT_MAX_TEXT_BYTES` of output, killing the child if it writes more. A child killed for time or output, or exiting 11, is `extract.ErrLimitExceeded`; a crash or an unknown exit code is `extract.ErrMalformed` with the child's stderr (at most 4 KiB). The job's own cancellation comes back as the context error, which is retried.
- **Memory.** The child polls its own Go runtime memory (`runtime/metrics`: everything mapped, minus what was returned to the OS) every 2 ms and exits 11 once it passes half of `EXTRACT_PDF_MAX_MEMORY`; it also sets the Go soft memory limit to that half so the collector works hard before then. Half, because the library grows slices by doubling, so one allocation can almost double use between two polls; with the trip point at half, peak use stays under the ceiling. Measured on macOS with the defaults: the self-referencing page tree fails in 0.09 s at 11 MB peak RSS, the two-billion `/Count` at once at 11 MB, and a 923 KB deflate bomb (600 MB of content) in 0.16 to 0.33 s at 300 to 380 MB. A real 26-page Berkeley City Council agenda takes 0.25 s at 19 MB.
- **Limits inside the child.** The child does not trust the page tree: it walks it itself, at most 32 levels deep and at most 4 × pages + 16 nodes, and rejects a tree whose `/Count` is more than the plausible number of pages, which is the smaller of 2000 and one page per 40 bytes of file. It stops at 200,000 glyphs on a page and at `EXTRACT_MAX_TEXT_BYTES` of text, and checks its context between pages. These catch the cheap cases quickly; the timeout and the memory ceiling catch the rest.
- **Finding the binary.** `EXTRACT_PDF_BINARY` when set; otherwise `extract` in the same directory as the running executable. The worker logs the path at startup and warns if nothing is there; PDF agendas are then cancelled with `extract.ErrUnavailable`. `go run ./cmd/worker` builds the worker into a temporary directory without the extractor, so run the worker locally with `make run-worker`, which builds every binary into `api/bin` and runs `api/bin/worker`, or set `EXTRACT_PDF_BINARY` to a built `extract`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `EXTRACT_PDF_BINARY` | `extract` next to the worker | Path to the `cmd/extract` binary. |
| `EXTRACT_PDF_TIMEOUT` | `60s` | Wall-clock limit on one PDF extraction, after which the child is killed. Must be positive. |
| `EXTRACT_PDF_MAX_MEMORY` | `536870912` | Memory ceiling for the child in bytes (512 MiB). The child exits at half of it. Must be positive. |
| `EXTRACT_MAX_TEXT_BYTES` | `8388608` | Largest text one PDF may produce, in bytes (8 MiB). Must be positive. |

How the text is laid out, and what is not read:

- **Lines.** Glyphs on the same baseline (within half the font size) form a line, and lines run top to bottom. Within a line the glyphs keep the order of the page's content stream; they are not sorted by x position, because the library computes wrong positions for some glyphs, and sorting by them scrambled words on real agendas (on a Berkeley agenda, "To request to speak, use the raise hand function" came out as "To rsuet eqto spkea, use the s re aihd"). A space is inserted where the gap to the previous glyph is wider than 0.15 of the font size, or where the text jumps back more than twice the font size (a new column on the same baseline). When a font gives no usable glyph width, the width is taken as half the font size and the gap must be 0.45 of it. Some words still come out with a stray space where a font's widths are wrong.
- **Not read.** Text inside form XObjects (content drawn with the `Do` operator), which the library does not follow; scanned pages without a text layer (there is no OCR); and PDF 2.0 files, whose header the library rejects. A document where more than half of the pages give no text is `extract.ErrNoText`, so a mostly empty extraction is never stored as an agenda. Bytes before the `%PDF-` header are skipped.

## Meeting events

`meeting_events` (migration `00006`) is the outbox between the pipeline and delivery: RSS feeds and webhooks read what happened to a meeting from here and nowhere else. Each row has a unique `id` (uuid, the public identifier of the event), a `sequence` (`bigint GENERATED ALWAYS AS IDENTITY`, unique), a `type`, the `meeting_id`, a JSON object `payload`, and `occurred_at` (`clock_timestamp()` at insert). Events are only inserted; nothing updates them. `meeting_id` is a plain foreign key, so a meeting that has events cannot be deleted until its events are, and undelivered events never vanish with their meeting.

| Type | Recorded by | Payload |
| --- | --- | --- |
| `agenda_published` | `AddAgendaVersion` / `AddAgendaVersionTx`, when it creates version 1 | `{"agenda_version_id": "<uuid>", "version": 1}` (`store.AgendaEventPayload`) |
| `agenda_changed` | `AddAgendaVersion` / `AddAgendaVersionTx`, when it creates version 2 or later | `{"agenda_version_id": "<uuid>", "version": n}` |
| `transcript_ready` | the job that finishes a transcript, through `RecordMeetingEventTx` | chosen by that job; it must be self-contained, since the outbox does not reference a transcripts table |

An event is written in the same transaction as the change it describes, so a consumer never sees an event for a change that rolled back, and a committed change always has its event; if the event insert fails, the version is rolled back with it. An unchanged agenda adds no version and so no event. The outbox has no deduplication key: every call records a new event. The job that records `transcript_ready` must do so only when a transcript actually became ready, in the transaction that makes it so, and not again on a retry that changes nothing.

`internal/store/events.go`:

| Function | Does |
| --- | --- |
| `RecordMeetingEventTx(ctx, tx, in)` | Inserts an event from `MeetingEventInput{Type, MeetingID, Payload}` in the caller's transaction and returns it. `Payload` is marshalled to JSON and must be an object (a struct or map); a payload that marshals to JSON `null` (`nil`, a nil pointer or a nil map) stores `{}`. An unknown type is `ErrUnknownEventType` and a non-object payload `ErrInvalidPayload`, both before the database is touched. A missing meeting is `ErrNotFound`; the failed insert has then aborted the caller's transaction, which must be rolled back. There is no variant without a transaction, because an event must commit with its change. |
| `ListMeetingEvents(ctx, meetingID)` | A meeting's events, in `sequence` order. Empty for a meeting with none. |

The table's check constraints accept only the three types and only a JSON object payload. The unique constraint on `sequence` and the index on `(meeting_id, sequence)` serve readers of all events and of one meeting's events in sequence order.

**Sequence order is assignment order, not commit order.** `sequence` is taken from the identity when the row is inserted, and transactions commit in any order: a transaction that inserted sequence 41 can commit after another that inserted 42. A reader that has seen 42 may therefore see 41 appear later. A consumer must not treat "the highest sequence seen" as a safe cursor without handling transactions still in flight, or it skips events for good. `occurred_at` has the same problem and the uuid `id` has no order at all. The RSS feed does not page through the outbox at all; it reads the newest events of one agency on every request (see "Agency RSS feeds"). How webhooks track what they have delivered belongs to CIV-46.

## Recordings and transcripts

Migration `00007` adds two tables.

- `recordings`: one row per recording of a meeting, unique on `(meeting_id, source_url)`, so a meeting split into several videos has several rows. `source_url` is the original recording URL and cannot be empty. `audio_key` is the [object storage](#object-storage) key of the downloaded audio and `audio_expires_at` is when that audio is due for deletion; both are null until the audio is stored, and a check constraint keeps them null or set together. `audio_deleted_at` is set when the retention job deletes the audio, so deleted audio is told apart from audio never downloaded; a check constraint allows it only while there is no audio. `seq` is an identity column that gives the order recordings were added, even within one transaction, where `created_at` is the same for every row. A partial index on `audio_expires_at` covers only rows with audio, for the retention job.
- `transcripts`: at most one per recording (unique `recording_id`). `full_text` is the whole transcript, `segments` a JSON array of `{"start_ms", "end_ms", "text", "speaker"}` objects (`speaker` left out when empty; a check constraint requires an array), and `summary` the nullable AI summary.

Both have `created_at`/`updated_at`. Neither cascades: rows are never deleted by the store.

`internal/store/recordings.go` and `internal/store/transcripts.go`:

| Function | Does |
| --- | --- |
| `UpsertRecording(ctx, in)`, `UpsertRecordingTx` | Adds a recording from `RecordingInput{MeetingID, SourceURL}`, or returns the existing one for that meeting and URL without writing to it, with `created` saying which. A missing meeting is `ErrNotFound`. |
| `GetRecording(ctx, id)` | One recording, or `ErrNotFound`. |
| `ListRecordings(ctx, meetingID)` | A meeting's recordings in the order they were added (by `seq`). Empty for a meeting with none. |
| `SetRecordingAudio(ctx, id, RecordingAudio{Key, ExpiresAt})`, `SetRecordingAudioTx` | Records the stored audio and clears `AudioDeletedAt`. An empty key or zero expiry is `ErrInvalidAudio`, returned before the database is touched. A missing recording is `ErrNotFound`. |
| `AddRecordingAudio(ctx, id, RecordingAudio{Key, ExpiresAt})`, `AddRecordingAudioTx` | Records the stored audio only while the recording has none and none was deleted, and clears any recorded download failure. Otherwise nothing changes and the error is `ErrAudioChanged`. An empty key or zero expiry is `ErrInvalidAudio`; a missing recording is `ErrNotFound`. |
| `RecordDownloadFailure(ctx, id, DownloadFailure{Kind, RetryAfter})` | Adds one to `download_attempts` and records the kind, the time and when to retry (nil for never). A kind outside the list in [Failures](#failures) is `ErrInvalidFailure`; a missing recording is `ErrNotFound`. |
| `MarkRecordingAudioDeleted(ctx, id, key)` | After the audio object is deleted, sets the key and expiry to null and `AudioDeletedAt` to now, only if the recording's key is still `key`. If audio was stored again in between (or already marked deleted), nothing changes and the error is `ErrAudioChanged`. A missing recording is `ErrNotFound`. |
| `ListRecordingsWithExpiredAudio(ctx, now, limit)` | Up to `limit` recordings that still have audio expiring at or before `now`, soonest expiry first. A `limit` below 1 is `ErrInvalidLimit`. |
| `SaveTranscript(ctx, in)`, `SaveTranscriptTx` | Stores `TranscriptInput{RecordingID, Text, Segments}` as the recording's transcript, replacing any earlier one in place (same id), and says whether it was created. The summary is kept when the text is unchanged and cleared otherwise. Saving the same text and segments again writes nothing and leaves `UpdatedAt` alone. A missing recording is `ErrNotFound`. |
| `GetTranscript(ctx, recordingID)` | The recording's transcript, or `ErrNotFound`. |
| `SetTranscriptSummary(ctx, transcriptID, textHash, summary)`, `SetTranscriptSummaryTx` | Sets the AI summary and returns the updated transcript, only if `textHash` (`store.HashContent` of the text that was summarised) still matches the transcript's text. If the transcript was saved with other text meanwhile, nothing changes and the error is `ErrTranscriptChanged`. A zero hash is `ErrZeroContentHash`. A missing transcript is `ErrNotFound`. |

Like `AddAgendaVersion`, the upsert and save check the parent row first (`SELECT ... FOR KEY SHARE`, which also keeps it from being deleted until the transaction ends) instead of relying on the foreign key. So `ErrNotFound` from `UpsertRecordingTx` or `SaveTranscriptTx` leaves the caller's transaction usable.

The summary check hashes the stored text in Postgres (`sha256` of its UTF-8 bytes) and compares it with the caller's `store.HashContent`, so the caller never sends the text back. A transcript's `UpdatedAt` would not do: setting the summary changes it too.

`Recording.Download` holds the download state from migration `00010` (attempts, last failure kind, its time, when to retry). A `Recording` without stored audio has a nil `Audio`; `AudioDeletedAt` is set when its audio was stored and later deleted. `store.TranscriptSegment` has the same fields as `transcribe.Segment` (`Start`, `End`, `Text`, `Speaker`), so the store does not import the transcription client. Times are stored to the millisecond, rounded. A segment with a negative start or an end before its start is `ErrInvalidSegment`, returned before the database is touched. Saving no segments stores an empty array, which reads back as an empty, non-nil slice.

`api/migrations/down_integration_test.go` also rolls back to `00006`, checks both tables are gone and `meeting_events` is not, and applies `00007` again. It also rolls back to `00009`, checks the four download-state columns of `00010` are gone and `users` is not, and applies `00010` again.

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

The gateway path always re-encodes through `ffprobe` and `ffmpeg` (which must be on `PATH`; the worker image installs Debian's `ffmpeg` package, see [The worker image](#the-worker-image)), unless the file is an `.mp3` at or under `TRANSCRIPTION_DIRECT_UPLOAD_MB`. Chunks are 10 minutes long and overlap by 5 seconds, so each starts 9 minutes 55 seconds after the previous one. Each chunk is re-encoded as 16 kHz mono 32 kbps MP3 (about 2.4 MB, about 3.2 MB as base64). A chunk over `TRANSCRIPTION_MAX_CHUNK_MB` fails with `transcribe.ErrChunkTooLarge`. The `groq` provider uploads a file at or under `TRANSCRIPTION_MAX_UPLOAD_MB` as is and chunks larger ones.

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

Number a new file above every migration already on `main`, and renumber a branch's migration when another lands first. Goose refuses to apply a migration numbered below the database's current version, so a lower number would fail `migrate up` at deploy. Gaps are fine: `00005` is unused because recordings (`00007`) was renumbered after meeting events (`00006`) merged, and users (`00009`) skipped `00008`, which another branch held; that branch's recording download state became `00010` when users merged first.

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
- **Worker on Railway**, also in `.railway/railway.ts`: the `civic-scanner-worker` service runs the River worker (`api/cmd/worker`) against the same `Postgres` database. It builds `api/Dockerfile.worker` (`build.dockerfilePath`, relative to the `/api` root directory) from the same commit, with the same watch paths and Wait for CI. It has no start command: the image's entrypoint runs `/app/worker` as PID 1 (see [The worker image](#the-worker-image)).
  - `/app/migrate up` runs as its pre-deploy step too, so the worker never starts new code before its schema exists, whichever service deploys first. Both services may run it at once: `migrations.Up` holds a Postgres advisory lock, so the second run waits and then finds nothing to apply.
  - No health check and no public domain: the worker serves no HTTP. A deploy goes live once the container starts; a crash restarts it up to 5 times.
  - One replica. River elects a leader for periodic jobs, so more replicas would be safe, but one is enough for a daily check.
  - On a redeploy the old worker gets SIGTERM and 30 seconds (`drainingSeconds`) before SIGKILL. The worker stops fetching new jobs and gives running ones 25 seconds to finish. A job cut off stays running in the database until River's rescuer finds it stuck and retries it.
  - Its variables are `DATABASE_URL`, `APP_ENV` and `LOG_LEVEL`. `INGEST_CHECK_HOUR_UTC` keeps its default. No flag variables are set, so `ingest-check-agencies` and `ingest-download-audio` are off and the daily check does nothing until the flags are turned on (see [Feature flags](#feature-flags)). `ingest-summarize-agendas` is off too, and the worker has no `AI_GATEWAY_API_KEY`: until both are set, summary jobs complete without calling a model and the sweep does nothing; once both are, the sweep summarizes the versions left without one. Its `STORAGE_*` variables reference the `audio` bucket the same way the API service's do.
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
