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

Every route is under `/v1`. All are public except `GET /v1/me`, the two `POST /v1/billing/*` routes and `POST /v1/agency-submissions`, which need a WorkOS access token (see [Authentication](#authentication)). `openapi/v1.yaml` is the full contract. Errors are JSON `{"code", "message"}` bodies.

| Route | Returns |
| --- | --- |
| `GET /v1/healthz` | `ok`, or `degraded` with 503 when the database is unreachable |
| `GET /v1/agencies` | every live agency |
| `GET /v1/agencies/{slug}` | one live agency, the same shape as a list item |
| `GET /v1/agencies/{slug}/meetings` | one page of the agency's meetings, newest first |
| `GET /v1/meetings/{id}` | one meeting of a live agency, with its agency, latest agenda version and recordings with their transcripts |
| `GET`, `HEAD /v1/agencies/{slug}/feed.xml` | an RSS 2.0 feed of a live agency's latest meeting events, behind a flag (see "Agency RSS feeds"); the website serves it at `/agencies/{slug}/feed.xml` |
| `GET /v1/me` | the signed-in user (`id`, `email`, `createdAt`), creating it on first sight; behind the `accounts` flag |
| `POST /v1/agency-submissions` | submits an agency for review as the signed-in user and returns the pending agency; behind the `accounts` and `agency-submissions` flags (see [Agency submissions](#agency-submissions)) |
| `POST /v1/billing/checkout-session` | `{"url"}` of a Stripe Checkout Session for the paid plan; behind `accounts` and `billing` (see [Billing](#billing)) |
| `POST /v1/billing/portal-session` | `{"url"}` of a Stripe billing portal session for the user's Stripe customer; behind `accounts` and `billing` |

- **Not live is not found:** an unknown slug and a pending or rejected agency get the same 404 (`not_found`), so a caller cannot tell whether an unpublished agency exists. A live agency with no meetings returns an empty list.
- **Meeting items** carry `id`, `title`, `startsAt` (UTC), `status` (`scheduled`, `held`, `cancelled`) and `sourceUrl`, the agency's page for the meeting, left out when the source gave none. New optional fields, such as agenda or summary links, can be added later without breaking clients, which must ignore fields they do not know.
- **Pagination:** keyset, not offset. Meetings are ordered by `starts_at DESC, id DESC`; the id breaks ties between meetings that start at the same time. A page asks for `limit` (default 50, at most 200) and returns `nextCursor`, which is `null` on the last page. Passing it back as `cursor` returns the meetings strictly after the last one on the previous page, so a meeting added or changed between requests never makes another one skipped or repeated. `meetings_agency_starts_at_idx` (`agency_id, starts_at DESC, id DESC`) serves the query.
- **Cursors** are opaque to clients. Inside, a cursor is base64url (no padding) of a small JSON object holding the agency id and the last meeting's start time and id, followed by the first 8 bytes of its SHA-256. The checksum catches cursors that were truncated, edited or corrupted; it is not a secret, because a cursor only names a position in public data. A cursor that does not decode, fails its checksum, or belongs to another agency is a 400 (`invalid_cursor`), never a restart from the first page. A `limit` outside 1 to 200 or not an integer, and any other parameter the router cannot parse, is a 400 (`invalid_parameter`).
- **Order of checks:** a malformed `limit` is rejected before the agency is looked up. The cursor is checked after, since it is tied to the agency, so an unknown agency with a bad cursor is a 404.
- **Meeting detail:** `GET /v1/meetings/{id}` returns the list item's fields plus:
  - `agency`, the same shape as `GET /v1/agencies/{slug}`, so the meeting page needs one request.
  - `agenda`: the latest version (`version`, `documentUrl`, `summary`, `changeNote`, `foundAt`), or `null` before an agenda is published. `foundAt` is when the service first found the version (`created_at`), not the agency's publication date. `summary` and `changeNote` are `null` until written; the first version never has a change note, and neither does a version stored while `ingest-agenda-changes` was off. `documentUrl` is the agency's original document. The extracted agenda text is not returned: the meeting page shows the summary and links to the agency's document, and the text can run to hundreds of kilobytes. Older versions are not returned either.
  - `recordings`: every recording in the order found (`seq`), empty before there is one. Each has `sourceUrl`, the agency's original recording, and `transcript`, `null` until the recording is transcribed. A transcript has `summary` (`null` until written) and `segments` (`startMs`, `endMs`, `text`). Speaker labels are stored on each segment but not returned: the model labels speakers per ten-minute chunk (`c3/speaker_1`), so the same person gets a new label every chunk, and the labels stay hidden until speakers can be matched across chunks. Exposing them again is the optional `speaker` property on `TranscriptSegment` in `openapi/v1.yaml` and one line in `apiTranscript` (`internal/httpapi/meetings.go`); `TestGetMeetingWithEverything` and the integration test check that a stored label never appears in the response. There is no `updatedAt`: the row's `updated_at` also changes when the summary is written, so it cannot say when the text changed. The segments are the full timestamped transcript the meeting page shows, so they are returned inline; the full text is not, since it repeats the segments. A meeting split into several videos has several recordings, each with its own transcript.
  - Nothing about stored audio (key, expiry, deletion) is exposed: the recording link is always the agency's, whether or not we still hold the audio.
  - Transcript segments are returned inline and uncompressed (a three-hour meeting is roughly 400 KB), and stored source URLs are returned as stored, so a consumer rendering them as links must accept only `http` and `https`.
  - The 404 rule is the agencies': an unknown id and a meeting of an agency that is not live get the same `not_found`. An id that is not a UUID is a 400 (`invalid_parameter`), like any other parameter the router cannot parse.
  - `store.GetLiveMeetingDetail` reads it in three queries (meeting joined with its live agency, latest agenda version, recordings left-joined with transcripts) in one read-only `REPEATABLE READ` transaction, so the parts are consistent with each other and the query count does not grow with the number of recordings. The agenda and transcript it returns leave `Text` empty. The unique constraints on `(meeting_id, version)`, `(meeting_id, source_url)` and `recording_id` serve the queries.
- **Caching:** the JSON routes set no `Cache-Control` or validators, the same as `GET /v1/agencies`. A caching policy for the public JSON reads is a separate decision. The RSS feed has its own, below.

### Agency RSS feeds

`GET /v1/agencies/{slug}/feed.xml` is an RSS 2.0 feed of one live agency's meeting events, read from the outbox (see "Meeting events"). `HEAD` returns the same status and headers without the body. The website serves the same feed at `/agencies/{slug}/feed.xml` (see "Feed on the website" below), which is the address subscribers use.

- **Flag and configuration.** The route answers only when the `agency-rss-feeds` flag is on and `PUBLIC_WEB_URL` is set. Otherwise it returns the same 404 as an unknown agency, before touching the cache or the database. The JSON reads have no flag; the feed has one because the story asks for it and because subscribers keep a feed URL for good.
- **The flag is evaluated anonymously.** The `authenticate` middleware puts a signed-in user's id in the flag targeting context of every request. The feed handler drops it (`featureflags.Anonymous`) before checking `agency-rss-feeds`, so the answer is the same with or without a token. The 200 is `Cache-Control: public`, and a per-user answer could let a shared cache hand a staff-only feed to everyone. Nothing in the response depends on request headers other than `If-None-Match`, so there is no `Vary`.
- **Turn the website's pages on first.** Item links point at `{PUBLIC_WEB_URL}/meetings/{id}` and the channel link at `{PUBLIC_WEB_URL}/agencies/{slug}`. On the website those pages answer only while its `meeting-pages` and `agency-pages` flags are on (see [Web pages](#web-pages)); with either off, those links are 404s. Turn both on before the API's `agency-rss-feeds`.
- **Autodiscovery.** The website has its own `agency-rss-feeds` flag. With it on, the agency page carries `<link rel="alternate" type="application/rss+xml" title="{agency} meetings" href="/agencies/{slug}/feed.xml">` and shows an "RSS feed" link. The website cannot see the API's flag, so turn the two on together: with only the website's on, the link leads to a 404; with only the API's on, the feed works but is not advertised.
- **`PUBLIC_WEB_URL`.** The origin of the public website, such as `https://civicscanner.org`, optionally with a path prefix. The API reads it at startup and refuses to start when it is not an absolute `http` or `https` URL with a host, a port (if any) from 1 to 65535, and no user info, query or fragment. A trailing `/` is dropped. The Atom `rel="self"` link is `{PUBLIC_WEB_URL}/agencies/{slug}/feed.xml`, the website's address for the feed, so subscriptions do not depend on the API version or host.
- **Items.** One per event, newest first by `sequence`, from the agency's 50 newest events (`store.MaxFeedItems`). Each event type has its own case: `agenda_published` is `Agenda posted: `, `agenda_changed` is `Agenda updated: `, `transcript_ready` is `Transcript ready: `, each followed by the meeting title and its date. An event of any other type is left out. The `guid` is the event's uuid with `isPermaLink="false"`, so it never changes and never repeats. `pubDate` is the event's `occurred_at` in RFC 1123 form with a numeric zone, in UTC, which for `agenda_changed` is when the change was found; the channel's `lastBuildDate` is the latest `pubDate` among the items (not necessarily the first item's, since a change found earlier can be recorded later), so an unchanged feed renders byte for byte the same. Items stay in `sequence` order. Descriptions say which agency did what for which meeting and when, then the change note (`agenda_changed`) and the summary of the version the event names (both agenda types). Meeting times are shown in `America/Los_Angeles`, since every tracked agency is in Southern California; an agency elsewhere needs a time zone on the agency. A transcript item says only that the transcript is ready: the event payload is not read, so nothing a job puts in it is exposed. A meeting split into several recordings gets one transcript item per recording (see [Transcript summaries](#transcript-summaries)). Nothing else leaves the database: no agency, version or recording ids, storage keys, document URLs or agenda text.
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
- An `act` claim, present when a WorkOS admin impersonates the user, must be an object with a non-empty string `sub`, which becomes `Identity.Actor`; any `act` that is present sets `Identity.Impersonated`. Presence is what counts: `"act": null` makes the token invalid (401), and so do `"act": {}`, an empty, null or non-string `sub`, and any `act` that is not an object, so a malformed impersonation token can never pass as an ordinary session. Tokens without `act` are unaffected. The user is still `sub`, and no user is created for the actor. The admin gate refuses impersonated sessions; other write endpoints can use `Impersonated` and `Actor` to refuse or audit them.

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
- Every response of a signed-in route (`GET /v1/me`, the billing routes and `POST /v1/agency-submissions`, all statuses) carries `Cache-Control: no-store`. The `noStore` strict middleware sets it for the operations listed in `privateOperations`; add new signed-in operations there.
- The API has no CORS handling, so a browser on another origin cannot call it with a bearer token. The web app makes signed-in calls from its server (`callApiAsUser`, below); a browser never holds the token.

Users (`users`, migration `00009`):

- One row per WorkOS user, unique on `workos_user_id` (the token's `sub`), with our own `id` (uuid), a nullable `email`, `created_at` and `updated_at`. The WorkOS id is the only identity key; `GET /v1/me` returns our `id`, not the WorkOS id.
- AuthKit access tokens carry no email. The API reads an optional `email` claim, which the environment's [JWT template](https://workos.com/docs/authkit/jwt-templates) adds with `{"email": {{ user.email }}}`. Without the template `email` stays null; the API does not call the WorkOS user API, so it needs no WorkOS secret. Email is mutable data: it is not unique, a token with a different email updates it, and a token without one leaves it alone.
- `store.EnsureUser` reads the row first and writes only when the user is new or the email changed. The write is `INSERT ... ON CONFLICT (workos_user_id) DO UPDATE`, so concurrent first requests for one user create exactly one row and all get it back.
- `stripe_customer_id` (migration `00013`) is the user's Stripe customer, set by the first checkout and cleared only when Stripe no longer knows it (see [Billing](#billing)); nullable and unique. It is not returned by `GET /v1/me`.
- `role` (migration `00012`) is `user` or `admin`, `NOT NULL DEFAULT 'user'` with the `users_role_check` constraint, so every new user, and every user from before the column, is `user`. `GET /v1/me` returns it as `role`. Plan (CIV-37) is a later column.

### Admins

Admin-only routes answer 403 to everyone who is signed in but not an admin. Two layers enforce it: a routing-time gate that checks the caller before anything about the request is parsed, and a dispatch-time backstop that refuses any admin operation the gate did not let through.

- **The gate (routing time).** `adminGate` (`internal/httpapi/admin.go`) is a chi middleware on the root router, right after `authenticate` and last. With `adminOperations` empty it is a pass-through and does no route lookup. For each request it asks the router which route pattern the method and path match (`chi.Mux.Find`) and runs only when that pair belongs to an entry in `adminOperations` (operation id to method and route pattern, such as `GET /v1/admin/agencies`). It runs before the generated wrapper binds path and query parameters and before the strict handler decodes the body, so the auth outcome never depends on the request being well formed: a malformed request from someone who is not an admin is a 401 or 403, and only an admin sees the 400. It checks, in this order: the token (no token or an invalid one is 401 `unauthorized` with `WWW-Authenticate: Bearer`, one that cannot be checked 503, exactly as for `GET /v1/me`), the `accounts` flag for that user (off is 404 `not_found`, and no user is created), the user (`requireUser`, so a first request creates the user with role `user`; a database error is 500 `internal_error`), and the role. Only `role = 'admin'` passes; anything else is 403 `forbidden` (`{"code":"forbidden","message":"this route is for admins only"}`), and so is any token with an `act` claim, a WorkOS admin impersonating an admin. The token's own `role` claim is ignored, so a `user` whose token says `admin` is refused. Every response of a gated operation, refusals and 400s included, has `Cache-Control: no-store`, set before anything else runs. A refusal is logged at `info` as `admin route refused` with the operation and our user id.
- **The backstop (dispatch time).** `adminBackstop` is a strict middleware keyed by operation id, so it runs on the operation chi actually dispatched to, whatever method or path the request arrived with. For every operation in `adminOperations` it refuses with 403 `forbidden` and `Cache-Control: no-store`, and logs `admin operation reached without passing the admin gate` at `warn`, unless the gate put an admin in the context (`adminFrom`). It catches what the gate cannot see: the gate decides from the request's method and path, while chi dispatches on its route context, so a root middleware after the gate that rewrites them (`middleware.GetHead`, `StripSlashes`, a path rewriter) could route a request to an admin handler the gate never checked, and an entry whose pattern does not match its route would leave the gate blind. In both cases the backstop refuses everyone, admins included, until the routing is fixed. `TestRootMiddlewareOrderKeepsTheGateLast` pins the root middleware list, and fails with an explanation if anything is added after the gate; add new middleware before `authenticate`.
- **It fails closed.** An empty, unknown or differently cased role is not admin (`store.Role.IsAdmin` compares with `admin` exactly), an error loading the user is a 500, never a pass, and the role never comes from the token or the request. The token's own `role` claim (a WorkOS organization role) is ignored. A gated handler reads the admin with `adminFrom(ctx)`, which is false unless the gate ran for this request and let an admin through, so a handler whose route somehow escaped the gate still refuses instead of serving.
- **The gated set comes from the spec.** An admin operation must be both marked `x-admin: true` in `openapi/v1.yaml` and listed in `adminOperations`; one without the other fails the tests below. The marking is an extension; the generators ignore `x-` extensions. `TestAdminOperationsMatchTheSpec` loads the spec with kin-openapi (the parser oapi-codegen itself uses) and fails unless the operations marked `x-admin: true` are exactly the keys of `adminOperations`, with the same method and `/v1` path; every key is an operation in the spec; every operation that can answer `#/components/responses/Forbidden` is marked, and every marked one can; and `x-admin` is `true` or absent. `TestAdminOperationsAreRouted` checks that each entry's pattern is a route the router really has, so a typo cannot leave an admin route ungated. The spec is not embedded at run time: oapi-codegen would embed it as a compressed blob that changes with every spec edit, and the test catches the same mistakes before merge.
- **Freshness.** The role is read from Postgres on every request, in the same `SELECT` that loads the user (`store.EnsureUser`), with no cache. A promotion or demotion applies to the user's next request; a request already past the gate finishes. The token's lifetime and the web session play no part.
- **Adding an admin route** (CIV-48 is the first): in `openapi/v1.yaml` give the operation `x-admin: true`, `security: [bearerAuth: []]` and the `400` when it takes input, `401`, `403` (`#/components/responses/Forbidden`), `404` and `503` responses; add `"OperationId": {method: http.MethodGet, pattern: "/v1/..."}` to `adminOperations`, with the pattern exactly as in the spec (`{id}` placeholders included); in the handler, call `adminFrom(ctx)` and answer 403 when it is false. No route is admin-only yet, so the gate is tested on test-only routes with a typed path parameter and a JSON body, and through the real router by marking `GetMe` admin-only inside a test.
- **Request errors.** The router gives the strict server its own error handlers: a body that cannot be decoded is 400 `invalid_body` and a failure writing a response is 500 `internal_error`, both in the JSON error shape (they were plain text before; no existing route has a body, so no response a client could see before has changed). The decode handler is `requestBodyError` (`internal/httpapi/agency_submissions.go`): on the route `/v1/agency-submissions` it applies that endpoint's checks first (token, flags, then 413, 415 and the body rules, see [Agency submissions](#agency-submissions)); on every other route it writes the shared 400 `invalid_body` with the decoder's message, as `invalidBody` always did. One handler, so there is one place a body error is written, and the submission endpoint's `invalid_body` has the same `{"code", "message"}` shape (its `ValidationError` adds `fields` only for field errors). A bad parameter stays 400 `invalid_parameter`. Public routes get no `Cache-Control` on these errors; gated routes get `no-store` from the gate.
- **An unknown role in the database.** The constraint allows only `user` and `admin`. If a later migration adds a role while older code still runs, the older code reads it as not admin and `GET /v1/me` reports it as `user`.

Making someone an admin. There is no UI, no API route and no environment variable for it. The user signs in once (so the row exists), then an operator runs the worker's `set-role` subcommand inside the worker's container, which uses the service's own `DATABASE_URL`:

```sh
railway ssh --service civic-scanner-worker -- /app/worker set-role <user-id> admin
railway ssh --service civic-scanner-worker -- /app/worker set-role <user-id> user
```

- `<user-id>` is our id, the `id` that `GET /v1/me` returns. An email also works, matched without regard to case, but email is not unique and comes from the token, so the command refuses an email that more than one user has and lists their ids; prefer the id.
- The second form demotes. Both are idempotent: setting the role a user already has changes nothing, not even `updated_at`.
- It prints the user's id, the new and previous role, the email, the WorkOS user id and when the user was first seen, so the operator can check it was the right person. It exits 1 and changes nothing for a user that does not exist (it never creates one), an ambiguous email, a role other than `user` or `admin`, a database without migration `00012`, a database it cannot reach, or a missing `DATABASE_URL`; every such message ends in `nothing changed`. It locks the user's row with `lock_timeout` set to 5 seconds for its transaction only, so if another session holds the row it gives up with `another session is changing this user right now; nothing changed; try again` instead of waiting. `/app/worker set-role --help` prints the usage.
- **The worker must be deployed with this command.** It needs a worker image that includes this change. An image from CIV-70 up to this change rejects `set-role` with a usage error and changes nothing, but an image from before CIV-70 ignores its arguments, so `/app/worker set-role ...` would start a second full worker inside the SSH session. Check that the deploy containing the command is live first.
- Why a subcommand rather than SQL: it validates the role and the target, refuses ambiguity, needs no `psql` or database credentials on the operator's machine, and its output is the audit line. The equivalent SQL, for when the worker cannot run, is `UPDATE users SET role = 'admin' WHERE id = '<user-id>';` (or `'user'` to demote) through `railway connect Postgres`; the constraint rejects any other value.
- Who is an admin: `SELECT id, email, updated_at FROM users WHERE role = 'admin';` through `railway connect Postgres`. Nothing else records who changed a role or when, beyond `updated_at` and the operator's terminal.

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
| `GET /auth/session` | `{"signedIn": false}` or `{"signedIn": true, "email": <string or null>, "role": <"user", "admin" or null>}`, `Cache-Control: private, no-store`. Calls `GET /v1/me` for the email and role. When the proxy kept a session it could not verify (the WorkOS key set or a refresh was unavailable), it answers signed in without an email or role rather than signed out; role is also null whenever `GET /v1/me` did not answer 200. The role is a display hint only: nothing on the web uses it yet, null is not admin, and the API decides every admin request itself. |

Sign-out is the server action `signOutAction` (`web/src/lib/auth/actions.ts`), a POST from a form, never a link. It deletes the session cookie and any verifier cookies and redirects to WorkOS's logout URL for the session with `returnTo` set to the site root, `<origin of NEXT_PUBLIC_WORKOS_REDIRECT_URI>/` (never taken from the request), which ends the WorkOS session and sends the browser home. That exact URL (for example `https://civicscanner.org/`) must be allowed as a sign-out redirect in the WorkOS dashboard, or WorkOS refuses the logout redirect. Next.js checks the action's `Origin` against the host, and the cookie is `SameSite=Lax`, so another site cannot sign a user out.

Return paths: `returnTo` goes through `safeReturnPath` (`return-path.ts`) before it is sealed into the state. The value is checked, normalised with the URL parser (dot segments resolved, the fragment dropped), and the normalised path and query are checked again, so `/.//host` and `/x/..//host`, which normalise to `//host`, are refused. A path is kept only if it starts with a single `/`, has no backslash, space or control character, is at most 2,048 characters, and, once percent-decoded, does not start with `//`, contain a backslash or a `.` or `..` segment, or lie under `/auth` in any letter case; anything else, including absolute and `javascript:` URLs, becomes `/`. The callback also builds the final URL on the redirect URI's origin from only the path and query, so a return path cannot leave the site either way.

The proxy (`web/src/proxy.ts`) runs only for `/auth/session`, `/billing` and `/submit-agency` (`matcher: ["/auth/session", "/billing", "/submit-agency"]`; `/billing` and `/submit-agency` because their server actions call the API as the user, and `/submit-agency` also reads the session while rendering), and returns at once unless WorkOS is configured and the request has a session cookie. Then AuthKit's `authkit()` verifies the access token against the WorkOS key set (fetched once and cached by `jose`), refreshes it when it expires within 60 seconds, and passes the session to the route in request headers that never reach the browser; the configured redirect URI is passed explicitly, and client-sent `x-workos-*`, `x-redirect-uri` and `x-civic-session-state` headers are dropped. The proxy also tells the route, in `x-civic-session-state`, whether it refreshed the session in this request (`refreshed`) or kept a cookie whose session it could not verify (`unverified`). It never sets a sign-in verifier cookie on this route. Other pages, the RSS route and static assets never go through it, so the route table is unchanged: `/` stays static and `/agencies` and the feed stay dynamic as before (see [Agency submissions](#agency-submissions)).

The header (`web/src/components/account-menu.tsx`) is rendered by the root layout only when the header is reserved as above. Without it the visible HTML is unchanged; the page payload still references the account menu's client chunk and carries a null child where the header would be. The header is one line high (`h-6`) in every state, so nothing on the page moves when the session arrives. It is a client component with no props: after hydration it fetches `/auth/session` and shows nothing until the answer arrives (and nothing if the request fails), so static pages stay static and nobody sees a flash of the wrong control. Signed out, it is a `Sign in` link (a plain `<a>`, so nothing prefetches it and starts a flow) with the current path and query as `returnTo`. Signed in, it is a button labelled with the email, or `Account` without one, that discloses a panel with a `Sign out` button (`aria-expanded` and `aria-controls`; opening it moves focus to `Sign out`; Escape closes it and returns focus to the button; focus leaving it or a pointer down outside closes it). With the `billing` flag on, the panel also has a `Billing` link to `/billing` (see [Billing on the web](#billing-on-the-web)). It uses the page's underline link style; there is no mockup, and no account or dashboard page. Cost: about 1.3 KB gzipped of first-load JavaScript on every page even with sign-in off, and with sign-in on one `/auth/session` request per full page load, which for a signed-in user also calls `GET /v1/me`.

Calling the API as the user (`web/src/lib/auth/api.ts`, server only):

```ts
const me = await callApiAsUser((api) => api.GET("/me"));
```

- `api` is the generated `openapi-fetch` client on `NEXT_PUBLIC_API_BASE_URL` with `Authorization: Bearer <access token>` from the session. Its fetch refuses any URL outside that origin and path, so the token cannot go to another host, and it uses `redirect: "manual"` (a redirect is a failure, never followed), `cache: "no-store"` and a 10-second timeout.
- The result is `ok` with the data, `signed-out` (no session; or a 401, a refresh with `refreshSession`, and a second 401; or a refresh token WorkOS rejects; or a 401 for a token the proxy refreshed in this same request, in which case it does not refresh again, since `refreshSession` reads the request's cookie and would replay the refresh token the proxy has just spent), `unavailable` (a 503 from the API, or a refresh that failed for a transient reason; the user stays signed in), `not-found` (a 404: accounts are off for this user), `conflict` with the parsed error body (a 409), `rejected` with the status and the parsed error body (a 400, 413, 415, 422 or 429) or `failed` (anything else, including an unreachable API). `conflict` and `rejected` mean the API understood and refused the request, so they are not logged; each caller maps them to its own message (billing's 409 says already subscribed or no subscription, a submission's 409 says the website is already on file). On `signed-out` after its own refresh, the session cookie is deleted; after a proxy refresh it is left alone, so the response carries one session `Set-Cookie`, the proxy's. A call that throws or ends in an unexpected status is logged once at `warn` with the error name or status only, never the token or URL.
- It needs the AuthKit proxy on the route (add the path to the matcher) and a context that can write cookies, a route handler or a server action, because a refresh rotates the refresh token and must store the new one. Do not call it while rendering a server component.

What the browser stores (privacy):

- `wos-session`: `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` whenever the redirect URI or the request is `https`, 400-day max age (the tokens inside expire much sooner). Its value is sealed with iron-session (AES-256-CBC with an HMAC-SHA256, keyed by `WORKOS_COOKIE_PASSWORD`) and holds the WorkOS access token, the refresh token, the WorkOS user object (WorkOS user id, email, whether it is verified, name, first and last name, profile picture URL, locale, last sign-in time, created and updated times, external id, metadata), the impersonator's email and reason when a WorkOS admin impersonates the user, and the authentication method. JavaScript cannot read it, and the tokens are never in page HTML, props, JSON responses, URLs, `localStorage` or logs.
- `wos-auth-verifier-<hash>`: the same attributes, 10 minutes, only during sign-in: a nonce, the PKCE code verifier, the return path and the redirect URI, sealed the same way.
- What the app reads about the user: the session's access token, to call the API, and the email and role the API returns from `GET /v1/me`. The header shows only that email. The app stores nothing else, and sends nothing about the user to analytics (see below).

Not verified: no WorkOS environment exists yet (CIV-3), so sign-in has not been run end to end against WorkOS. Most tests mock the SDK; `web/src/lib/auth/sdk.test.ts` runs the real SDK through the sign-in and callback routes against a local stub of the WorkOS API (`WORKOS_API_HOSTNAME`, `WORKOS_API_HTTPS`, `WORKOS_API_PORT`) that serves a key set and the code exchange, and checks the authorization URL, the ignored `x-redirect-uri`, the verifier and session cookie attributes, the sealed return path round trip with hostile values, and that a mismatched or missing verifier is refused without contacting WorkOS. `vitest.config.ts` inlines the SDK so its `next/headers` import can be mocked.

## Agency submissions

A signed-in user can suggest an agency by name and website. The suggestion becomes an agency with status `pending`, recorded as submitted by that user and when. Nothing reviews it yet: approving or rejecting is CIV-48, screening the site is CIV-47.

### The endpoint

`POST /v1/agency-submissions` with `{"name": "...", "website": "..."}` (`internal/httpapi/agency_submissions.go`). It is a collection of its own rather than `POST /v1/agencies` because `/v1/agencies` is the public, read-only list of live agencies; a submission is a private write whose result is never readable there, and a later admin story can list `/v1/agency-submissions` without touching the public routes.

Checks, in order, each before the next:

1. **Token.** No token or an invalid one is a 401 (`unauthorized`), a token that cannot be checked a 503, exactly as `GET /v1/me`. This holds even when the body is malformed or too large: the strict handler decodes the body before the operation runs, so its decode-error handler (`requestBodyError`) applies the same checks first.
2. **Flags,** evaluated for that user: `accounts` and `agency-submissions` must both be on, or it is a 404 (`not_found`) and no user row is created.
3. **Body.** A body over 16 KiB (`maxRequestBodyBytes`) is a 413 `body_too_large`. A `Content-Type` other than `application/json` (any letter case, other parameters allowed, a `charset` only if it is `utf-8`), or none, is a 415 `unsupported_media_type`, also when the body is malformed. Then the body must be exactly one JSON object whose members are only `name` and `website`, each a string and each at most once, with nothing but whitespace after it; anything else is a 400 `invalid_body` without `fields`, whose message says which rule failed (not an object, a member that is not a string, an unknown member, a repeated member, data after the object). Member names are matched exactly, so `Name` is an unknown member. A missing member counts as empty and is reported by the field checks as `required`. The router's `captureBody` middleware reads every request body once, through `http.MaxBytesReader`, keeps the bytes and the `Content-Type` in the request context, and hands the next handler a copy of the same bytes (a body over the cap, or one that fails to read, becomes a reader that returns that error, so any decoder fails on it); `readSubmissionBody` (`internal/httpapi/body.go`) checks them with `encoding/json`'s token reader, which sees duplicate keys and trailing data that `Unmarshal` would silently accept. The generated binding only decides that a body is malformed, and its error handler then runs the same token and flag checks first, so the order holds either way.

   **Where `captureBody` sits.** The root middleware is `RequestID`, `requestLogger`, `recoverer`, `joinHeaderLines`, `captureBody`, `authenticate`, `adminGate`, pinned by `TestRootMiddlewareOrderKeepsTheGateLast`. `captureBody` is before `authenticate` and the admin gate, so the gate is still last and still sees the method and path exactly as they arrived. Its position is safe because it decides nothing: it never writes a response, never rejects a request, and never changes the method, path, headers or the identity; it only caps the body at 16 KiB and replaces it with an identical copy. Reading before authentication costs at most 16 KiB per request, the same as the cap did before. Bodyless requests (every GET, and billing's POSTs) read zero bytes. A route on main that reads its own body (the admin test routes, any later webhook) gets the same bytes it would have read, as long as they fit the cap; a route that needs a larger body must raise `maxRequestBodyBytes` or exempt itself here.
4. **Fields** (`internal/submission`). Each invalid field gets one entry in `fields` (`field`, `code`, `message`) of a 400 `invalid_submission`; the codes are listed on `FieldError` in `openapi/v1.yaml` and the messages are written to be shown next to the field. Nothing is written for an invalid submission.
5. **The user** is created on first sight, as for `GET /v1/me`.
6. **Store.** A website already on file is a 409 `already_on_file` that says nothing about the agency on file; a user at the limit gets a 429 `submission_limit_reached`. Otherwise 201 with `id`, `name` and `website` as stored, `status: "pending"` and `submittedAt`.

The response never includes the pending agency's slug or the WorkOS user id. Every response carries `Cache-Control: no-store`.

### Name

Plain text: U+FFFD (the replacement character, which is also what the JSON decoder turns invalid UTF-8 into, so `"Big \xff\xfe"` over HTTP is refused rather than stored as `Big ��`), control characters, Unicode format characters (category Cf: bidi controls, zero-width characters, byte order marks, the tag block), private-use and unassigned code points (including the noncharacters) are refused (`invalid_characters`). Runs of whitespace (any Unicode space, tabs and line breaks) become one space and the ends are trimmed. Then it must be 2 to 200 characters (code points) and contain at least one letter or digit. Markup characters such as `<` are allowed and stored as typed: every page renders names as text. Refusing Cf also refuses the zero-width joiners some scripts and emoji sequences use. The name is not unique and is not compared with live agencies' names; it is only a label for the reviewer.

### Website

The website must be an `http` or `https` URL on a public host name:

- Surrounding whitespace is trimmed; whitespace or control characters inside, U+FFFD or invalid UTF-8 (`invalid_url`), or more than 2,048 bytes (before or after normalising) are refused.
- Without `://`, `https://` is assumed (`metro.net`, `metro.net:443/board`). A value with another scheme, or one that looks like a scheme without `//` (`javascript:`, `mailto:`, `data:`, `http:/x`), is `unsupported_scheme`.
- No user name or password (`credentials`).
- No IP literals of any form (`ip_address`): IPv4, bracketed IPv6, and the numeric forms some resolvers accept (`127.1`, `2130706433`, `0x7f000001`), which all end in a numeric label.
- The host is converted to its ASCII (punycode) form with IDNA lookup rules, which also lower-cases it; a trailing dot is dropped. It needs at least two labels, each 1 to 63 letters, digits or hyphens, not starting or ending with a hyphen, at most 253 bytes, ending in an alphabetic top-level domain of at least two letters or an `xn--` one (`invalid_host`). Underscores are refused.
- `localhost` and the special-use and private top-level domains `localhost`, `local`, `localdomain`, `internal`, `intranet`, `lan`, `home`, `corp`, `private`, `test`, `example`, `invalid`, `onion`, `arpa` (so `home.arpa`) and `alt` are `not_public`.
- A port is allowed only when it is the scheme's default (`http` 80, `https` 443) and is dropped; any other port is refused (`port`).

The stored website is `scheme://host` plus the path with dot segments resolved and trailing slashes removed, without the query, the fragment or the default port: `HTTP://WWW.Metro.NET/about/?utm=x#top` is stored as `http://www.metro.net/about`. The scheme is kept as submitted.

**Same website.** Two websites are the same when they name the same site, and an agency's site is its host name: the scheme, letter case, a leading `www.`, a trailing dot, a default port, the path, the query and the fragment do not count. The comparison key is the ASCII host with one leading `www.` removed (unless only one label would remain, so `www.com` stays `www.com`). So these are all the same site, `metro.net`:

| Submitted | Key |
| --- | --- |
| `https://www.metro.net` | `metro.net` |
| `metro.net` | `metro.net` |
| `HTTP://WWW.Metro.NET:80/` | `metro.net` |
| `https://metro.net/about/board?ref=1` | `metro.net` |
| `https://metro.net.` | `metro.net` |

and these are different sites: `boards.metro.net`, `www2.metro.net`, `metro.org`. `münchen.de` and `xn--mnchen-3ya.de` are the same. Treating the host as the site means two agencies that live under one domain (a city's site with a page per agency) cannot both be submitted; the second gets `already_on_file`.

The key is compared with every agency on file, whatever its status: live, pending or rejected. A rejected agency's site cannot be submitted again.

### Storage

Migration `00014` adds two tables and changes none:

- `agency_websites (agency_id, site_key)`: one row per agency with a website, `site_key` unique (`agency_websites_site_key_unique`) and not empty. The migration backfills it from every existing agency's `website` (see below), so the six seeded agencies get `metrolinktrains.com`, `metro.net`, `goventura.org`, `rctc.org`, `gosbcta.com` and `octa.net`.
- `agency_submissions (agency_id, submitted_by, submitted_at)`: who submitted the agency and when. `submitted_by` references `users` with no cascade, so deleting a user who submitted agencies fails until their submissions are dealt with; `agency_id` cascades with the agency. Indexed by `submitted_by` for the limit.

`store.CreateAgencySubmission` runs in one transaction: it locks the user's row (`SELECT ... FOR UPDATE`), counts that user's submissions that are still `pending`, and inserts the agency, its `agency_websites` row and its `agency_submissions` row. The unique constraint is what rejects a duplicate site, so two concurrent submissions of one site cannot both succeed: the second waits for the first's transaction and then fails with a unique violation, which becomes `ErrWebsiteOnFile` and rolls back its agency row. The row lock serialises one user's submissions, so concurrent requests cannot overshoot the limit. Tests run twelve concurrent submissions of one site (one succeeds) and fifteen concurrent submissions by one user (five succeed).

**The backfill.** One `DO` block in the migration computes each existing agency's key with a simple SQL form of the rule: lower-case the website, remove the scheme and a leading `www.` when another dot follows, and cut everything from the first `/`, `:`, `?` or `#`. It does no IDNA conversion and none of the validation. That is enough for rows that exist when it runs: the six seeded agencies have plain ASCII `https` origins, nothing else has written agencies in production, and every later row gets its key from the Go normaliser. A website with non-ASCII host letters would get a Unicode key that the Go side, comparing punycode, would not match; there is none. Then:

- If two **live** agencies get the same key, the migration fails with `agency_websites backfill: live agencies <ids> share the site <key>; fix their websites before migrating`, and nothing is applied. Two live agencies must not be merged silently; an operator fixes the data and deploys again.
- Otherwise one agency per key gets the row: a live one first, then the oldest (`created_at`, then `id`). Each other agency with that key is left without a row and named in a `RAISE NOTICE` (`agency_websites backfill: <status> agency <id> left without a site row because agency <id> holds the site <key>`), which `migrate` prints in its log. Such a pending or rejected agency is then not protected against duplicates; review it by hand.
- An agency with an empty website gets no row.

`TestAgencySubmissionsMigrationBackfillsSiteKeys` and `TestAgencySubmissionsMigrationRefusesLiveAgenciesSharingASite` cover the preference, the notices and the failure.

> **Invariant for CIV-48 and every future agency writer: every agency with a website has exactly one `agency_websites` row, written in the same transaction as the agency, with the key from `submission.NormalizeWebsite`.** Without it the agency is not protected against duplicate submissions, and a later submission of the same site succeeds. Today only the seeds (via the backfill) and `store.CreateAgencySubmission` create agencies. Approving a submission only changes its status and keeps the row; changing an agency's website must update the row; an admin path that creates live agencies directly must insert one and handle `ErrWebsiteOnFile`. There is no trigger enforcing it. `TestEveryLiveOrPendingAgencyTheStoreKnowsHasASiteRow` (`internal/store`) fails if a live or pending agency created by the store's own paths, or a seeded one, has no row; extend it when adding a create path.

**Slug.** A pending agency gets the slug `submitted-` followed by the 32 hex digits of its id. The slug is never derived from the submitted name, so a submission cannot take, shadow or squat the slug of a live agency or one a source adapter is registered for, and a pending agency's slug cannot be guessed. CIV-48 should give an approved agency a real slug when it goes live.

### Limits

- At most 5 submissions awaiting review per user (`store.MaxPendingSubmissionsPerUser`). Only `pending` submissions count: once one is approved or rejected, the user can submit another. There is no time window, so waiting does not help, and the 429 has no `Retry-After`.
- Request bodies of at most 16 KiB on every route (`captureBody`).
- A duplicate (409) or an invalid submission writes nothing and does not count toward the limit, so it is not limited either: a signed-in user can ask whether any number of sites are on file, one request at a time. The answer is one bit per site and never names the agency.
- No CAPTCHA and no rate limit per IP.

### Not fetched

The submitted website is never fetched, resolved or linked to by this story: it is stored as text and shown back to its submitter as text. Fetching it is CIV-47's screening job, and that is where the SSRF risk lies: the validation here refuses IP literals and private names, but a public host name can still resolve to a private, loopback, link-local or cloud metadata address (`1.2.3.4.nip.io`, or DNS that changes between check and use), and a public site can redirect to one. The screener must check the resolved address of every connection, including after redirects, and must not rely on this validation.

### Never public

A pending (or rejected) agency appears nowhere public. Every read of agencies filters on `status = 'live'`, and meetings only exist for agencies `check_agency` has checked, which it does only for live ones:

| Path | How it is kept out | Tested with a real submission in |
| --- | --- | --- |
| `GET /v1/agencies` | `store.ListAgencies` lists live agencies only | `internal/store`, `internal/httpapi` (integration) |
| `GET /v1/agencies/{slug}`, `/meetings` | `store.GetLiveAgency` is live only; the meetings route looks the agency up first | same |
| `GET`, `HEAD /v1/agencies/{slug}/feed.xml` | `store.GetLiveAgencyFeed` is live only | same |
| `GET /v1/meetings/{id}` | `store.GetLiveMeetingDetail` joins the live agency | same, with a meeting inserted for the pending agency |
| Web `/`, `/agencies`, the coming-soon lookup | built from `GET /v1/agencies`; the lookup asks only about agencies in that list | the web reads only the API |
| Web `/agencies/{slug}`, `/meetings/{id}`, the website's feed route | read the routes above | as above |
| Daily check (`enqueue_agency_checks`) | `store.ListAgencies` | `internal/jobs` (`TestSubmittedAgencyIsNeverChecked`) |
| `check_agency` | skips an agency that is not live, even one with an adapter for its slug | same |
| `worker check-agency <slug>` and `--all` | refuses a slug that is not live; `--all` lists live agencies | `cmd/worker` |
| Hourly sweeps (agenda summaries, transcriptions, transcript summaries) | join the live agency, and need meetings a pending agency cannot have | existing tests for agencies that are not live |

There is no sitemap. The 404 for a pending agency's slug or meeting is the same as for an unknown one, with or without a token.

### The web page

`/submit-agency` (`web/src/app/submit-agency/`), behind the web's `agency-submissions` flag and sign-in (`accounts` and a valid WorkOS configuration); with either off it is a 404. It is not under `/agencies/` so that no agency slug can collide with it. The page calls `connection()` first, so the answer is decided per request, not at build time.

- **Signed out.** No session cookie, or a session the proxy rejected, redirects to `/auth/sign-in?returnTo=%2Fsubmit-agency`, which comes back to the page after sign-in. A session the proxy kept but could not verify (WorkOS unreachable) gets a notice instead of the form, without signing the user out.
- **The form** (`_components/submission-form.tsx`) has two labelled text inputs, each with a hint and, when invalid, an error tied to it with `aria-describedby` and `aria-invalid`, plus an error summary (`role="alert"`) linking to each field. The inputs are `required` with the API's maximum lengths, as a convenience; the website is `type="text"` with `inputMode="url"` so `metro.net` is accepted. It is a client component using `useActionState` with the server action `submitAgency` (`actions.ts`); a form whose action is a server action also posts without JavaScript, and the page then renders with the action's result.
- **The action** checks the flag and sign-in again, checks that both fields are filled in and within the maximum lengths (counted as the API counts them), and calls `POST /v1/agency-submissions` through `callApiAsUser`, so the token stays on the server. Success shows the stored name and website with a note that the agency appears only if approved, and a link to suggest another; the notice is wrapped in `role="status"` with `tabIndex={-1}` and receives focus when it appears, so screen readers announce it and keyboard users land on it. A 400 shows the API's message for each field (or a general one when the body could not be read, and for a 413); a 409 says the website is already on file; a 429 says the user has reached the limit for now; a 404 from the API says suggestions are not open; anything else says the suggestion could not be sent. A session that ends meanwhile redirects to sign-in. The form keeps what was typed in every case but success.
- **The link.** `/agencies` shows "Suggest an agency" in its header, a plain `<a>` so following it is a full page load (a client-side navigation would fetch the page's sign-in redirect and then WorkOS's authorization URL with `fetch`, which fails on CORS before falling back to a full load), when the redirect URI is a valid callback URL and the `accounts` and `agency-submissions` flags are on, the same rule that shows the sign-in header, so a signed-out visitor sees it and is sent to sign in.
- **Turning it on.** Turn on the API's `agency-submissions` together with the web's, after `accounts` works on both sides. With only the web's on, the form says suggestions are not open.

## Billing

A signed-in user starts the single paid plan in Stripe Checkout and manages it in Stripe's billing portal. The API creates both sessions; the web app only redirects to the URLs it returns. `internal/billing` holds it. Nothing here makes anyone paid: returning from Checkout proves nothing, and the plan will come only from signature-verified Stripe webhooks (CIV-37). No code reads a paid status yet.

**Keep the `billing` flag off in production until CIV-37 ships.** Until then a user can pay and nothing on the site changes: the plan is never recorded, and nothing handles a duplicate or failed subscription.

| Route | Does |
| --- | --- |
| `POST /v1/billing/checkout-session` | Finds or creates the user's Stripe customer, refuses a user who already has a live subscription, then creates a Checkout Session in `subscription` mode for `STRIPE_PRICE_ID`, quantity 1, and returns `{"url"}`. |
| `POST /v1/billing/portal-session` | Creates a billing portal session for the user's stored Stripe customer and returns `{"url"}`. |

Checks, in order: the token (401, or 503 `auth_not_configured` / `auth_unavailable`, as for `GET /v1/me`), then the `accounts` and `billing` flags for that user (404 `not_found`, no user created), then whether billing is configured (503 `billing_not_configured`, no call to Stripe). Then the user is created on first sight, as for `GET /v1/me`. Other answers:

- 409 `already_subscribed` from checkout when the customer has a subscription in `active`, `trialing`, `past_due`, `unpaid` or `paused`. The client should send the user to the portal.
- 409 `no_billing_customer` from the portal when the user has never started checkout (Stripe is not called), or when Stripe no longer knows the stored customer (see [Customers](#customers)).
- 502 `billing_unavailable` when Stripe refuses or does not answer, or answers without an `https` URL. The body never carries Stripe's message.
- 500 `internal_error` for a database error.

Every response carries `Cache-Control: no-store` (both operations are in `privateOperations`). They are signed-in routes, not admin routes: neither is marked `x-admin` or listed in `adminOperations`, so the admin gate and backstop (see [Admins](#admins)) let them through to their own checks. There is no request body: the price, the customer and every URL come from the server.

### Configuration

| Variable | Meaning |
| --- | --- |
| `STRIPE_SECRET_KEY` | A secret (`sk_...`) or, better, restricted (`rk_...`) key. A secret: set it in Railway, where it is declared with `preserve()`. A restricted key needs at least write access to Customers, Checkout Sessions and the Customer portal, and read access to Subscriptions; confirm in test mode, since this was not checked against Stripe. |
| `STRIPE_PRICE_ID` | The recurring price of the paid plan (`price_...`). Not a secret. |
| `PUBLIC_WEB_URL` | The website's origin, shared with the RSS feed (see [Agency RSS feeds](#agency-rss-feeds)). Checkout returns to `{PUBLIC_WEB_URL}/billing/success` or `/billing/cancel`, and the portal to `{PUBLIC_WEB_URL}/billing`. Must be `https` with a live key. |

Billing is the only feature whose bad configuration does not stop the binaries. `config.Load` is shared by `api`, `worker` and `migrate`, and a half-set billing must not block migrations or take the API down, so a problem leaves billing **not configured** (the routes answer 503 `billing_not_configured`, Stripe is never called) and every binary starts. The API logs it once at startup at `error`, `billing is not configured: the Stripe settings are invalid`, with a `problem` naming the variable, never its value. Problems:

- one of `STRIPE_SECRET_KEY` and `STRIPE_PRICE_ID` set without the other;
- a key that has whitespace inside, does not start with `sk_` or `rk_`, or is a publishable `pk_` key. The check is a deny-list, so Stripe's other secret formats (such as `sk_org_...`) are accepted;
- a price that does not start with `price_`, or has whitespace;
- no `PUBLIC_WEB_URL`, or a live key (`_live_` in it) with a `PUBLIC_WEB_URL` that is not `https`.

A malformed `PUBLIC_WEB_URL` still stops the API at startup, as before, because the RSS feed relies on it too.

The key is held in `config.Secret`, which prints as `[redacted]` through `fmt` (every verb, including `%#v`), `slog` (JSON and text handlers, via `LogValue`), `encoding/json` and `MarshalText`; only `Reveal()` returns it, and only `internal/billing` calls that. The other secrets in `config` (`AI_GATEWAY_API_KEY`, `GROQ_API_KEY`, `FIRECRAWL_API_KEY`, `STORAGE_SECRET_ACCESS_KEY`, `POSTHOG_API_KEY`) are still plain strings and would print if a whole config were logged; nothing logs one today. Moving them to `config.Secret` is a separate change across their packages.

#### Turning billing on in production

`.railway/railway.ts` sets `variables: { managed: true }`, so a variable added in the Railway dashboard is reverted by the next apply unless the file declares it. Only `STRIPE_SECRET_KEY` is declared (with `preserve()`, so its value is set in the dashboard and kept). `STRIPE_PRICE_ID` and `PUBLIC_WEB_URL` must be added to the API service in `.railway/railway.ts` as plain values, through a pull request. To never half-configure billing:

1. In the Stripe dashboard, do the steps under [Stripe dashboard](#stripe-dashboard) in the mode you will use.
2. Merge a change to `.railway/railway.ts` that sets `STRIPE_PRICE_ID` and `PUBLIC_WEB_URL` (`https`) on the API service, and approve its apply. Billing stays off: the key is missing, and the API logs `STRIPE_PRICE_ID is set without STRIPE_SECRET_KEY`.
3. Set `STRIPE_SECRET_KEY` in the Railway dashboard. The API redeploys and logs `billing configured` with the mode and price.
4. Only after CIV-37 has shipped, turn on the `billing` flag on both sides (API and web), with `accounts` already on.

To turn billing off, turn the flag off first. A key from the other mode than the stored customers (a live database with a test key, or the reverse) makes Stripe answer `resource_missing`, which clears and replaces customers as below, so never point a live database at a test key.

### Customers

- One Stripe customer per user, created by the user's first checkout, with the user's email when the token carried one and `metadata[civic_scanner_user_id]` set to our user id. It is stored in `users.stripe_customer_id` (migration `00013`, unique, never empty).
- Concurrent first checkouts: the create request's idempotency key is derived from the user and the create parameters, `civic-scanner-customer-<user id>-<hash>`, where the hash is the first 8 bytes of a SHA-256 of the user id and email. Requests with the same parameters get the same customer from Stripe within its 24-hour idempotency window. A changed email makes a new key, so a retry after the email changed is a new customer, not a permanent `idempotency_error` (Stripe refuses a reused key with other parameters). The store then sets the column only while it is null (`UPDATE ... WHERE stripe_customer_id IS NULL`) and reads it back otherwise, so the database decides: every request uses the one stored customer even when Stripe returned different ones, and the others are left unused in Stripe (they have no subscription). The unique constraint stops two users sharing a customer.
- A stored customer Stripe no longer knows (deleted in the dashboard, or a key from the other mode) answers `resource_missing` on `customer`. Checkout then logs it at `warn` with the old id, clears the column only if it still holds that id, and creates a fresh customer once in the same request, with the key `civic-scanner-customer-<user id>-<hash>-replaces-<hash of the old id>`, so it can never replay the dead customer and concurrent recoveries share one key. If the new customer fails too, the request is a 502; it is not retried again. The portal also clears the dead id but answers 409 `no_billing_customer` instead of creating a customer, because a new customer would have nothing to manage; the user starts checkout again. A `resource_missing` on any other parameter (a deleted price) is an ordinary 502 and clears nothing.
- Checkout and portal sessions get a fresh key per call (`civic-scanner-checkout-<user id>-<uuid>`), the same across that call's retries, so a retry never opens a second session and every click gets a new one.

### What CIV-37 can rely on

- Each Checkout Session carries `client_reference_id` and `metadata[civic_scanner_user_id]` set to our user id, and its `customer` is the user's stored customer. The subscription it creates carries the same metadata (`subscription_data.metadata`), so `customer.subscription.*` events map back to a user by metadata or by `users.stripe_customer_id`.
- `checkout.session.completed`, `customer.subscription.created`, `.updated` and `.deleted`, and `invoice.paid` / `invoice.payment_failed` are the events to handle. Nothing here listens to them.

### Duplicate subscriptions

Before creating a session for a customer that already existed, the API lists the customer's subscriptions and refuses a live one (statuses above; `incomplete` is not counted, so a user whose first payment failed can try again). This can be known without webhooks. It does not cover two Checkout Sessions opened before either is paid: both can complete. Stripe closes most of that gap itself: with the customer portal's login link on and "Limit customers to one subscription" turned on in the Checkout settings, Checkout sends a customer who already has an active subscription to the portal instead of taking a second payment ([Stripe: limit customers to one subscription](https://docs.stripe.com/payments/checkout/limit-subscriptions)). Turn that on in the Stripe dashboard. The gap remains even then (two sessions opened before either is paid, or the setting turned off), so **CIV-37 must handle a second live subscription** for one user, for example by refunding or cancelling it.

There is no per-user rate limit on session creation yet: each click creates a Checkout Session, and a scripted client could create many. Whether to add one is a pending product decision.

### Calls to Stripe

- [`stripe-go`](https://github.com/stripe/stripe-go) v87 through `stripe.NewClient` with its own backend: telemetry off, the SDK's logger silenced, the default HTTP client replaced. The API version is the one the SDK release pins (`stripe.APIVersion`, `2026-10-28.endive` in v87.1.0), sent as `Stripe-Version`; Stripe's typed SDKs cannot target another version ([Stripe: set a version](https://docs.stripe.com/sdks/set-version)). Upgrading the module upgrades the API version, so read Stripe's changelog for the version it pins before merging a major bump.
- Each attempt has a 4-second timeout, one retry (the SDK retries network errors, 409 and 5xx, and honours `Stripe-Should-Retry`), and the whole operation (up to three Stripe calls) an 8-second deadline, below the web app's 10-second API timeout. A 4xx is not retried.
- Failures are logged at `warn` as `stripe request failed` with the operation, HTTP status, Stripe error type, code, param and request id. Stripe's message, the key and session URLs are never logged; a successful checkout logs the session and customer ids at `info`.

### Stripe dashboard

Before turning billing on:

- Create the product and its recurring price; copy the price id to `STRIPE_PRICE_ID`.
- Save the customer portal configuration (Settings, Billing, Customer portal): what customers may do (update payment methods, cancel, switch plans) and the business links (terms, privacy policy). A portal session fails until the configuration is saved, in each mode.
- Turn on "Limit customers to one subscription" in the Checkout settings, with the portal login link enabled.
- Choose the payment methods, and whether Stripe Tax applies, in the dashboard. The API sets neither, so the dashboard's defaults apply.
- Prefer a restricted key with the permissions above.

### Tests

Nothing calls Stripe. `internal/billing/stripetest` is a fake Stripe API on `httptest`, with ids unique to each instance: customers (honouring idempotency keys, refusing a reused key with other parameters as `idempotency_error`, or ignoring keys on request), deletable customers and `resource_missing` for unknown ones, subscription lists, Checkout and portal sessions with a settable URL, queued failures, hanging requests, a barrier that holds concurrent customer creations until all have arrived, and, on request, Stripe's 409 for a second request whose key is still in flight. `billing.WithAPIURL`, `WithHTTPClient` and `WithoutRetryDelay` point the service at it. The tests check the exact form sent, idempotency keys, customer reuse, the duplicate-subscription guard, recovery from a dead customer, changed emails, the in-flight 409, `https`-only redirect URLs, retries, timeouts, generic errors and logs without secrets; the integration tests run concurrent checkouts against Postgres with and without Stripe's idempotency.

Not verified: no Stripe account exists yet (CIV-3), so nothing has run against Stripe, in test or live mode.

### Billing on the web

`web/src/lib/billing/` and `web/src/app/billing/`, behind the `billing` web flag and sign-in (the `accounts` flag and a valid WorkOS configuration). Off, every route below is a 404. The pages are temporary until the dashboard (CIV-26).

| Route | Shows |
| --- | --- |
| `/billing` | "Start the paid plan" and "Open the billing portal", each a form posting a server action. Without a session cookie it redirects to `/auth/sign-in?returnTo=%2Fbilling`. An `error` query parameter of `already-subscribed`, `no-subscription` or `unavailable` shows a fixed message; any other value is ignored. |
| `/billing/success` | Checkout's success URL. Says Stripe has received the payment and that the plan will show once Stripe's confirmation has been processed, with no promise of timing. It reads nothing from the URL and changes nothing. |
| `/billing/cancel` | Checkout's cancel URL. Says nothing was charged. |

The actions (`startCheckoutAction`, `openBillingPortalAction`) call the API with `callApiAsUser` and redirect: to the returned URL when it is `https` without credentials, to sign-in when signed out, to `/billing?error=already-subscribed` or `?error=no-subscription` on a 409, and to `/billing?error=unavailable` otherwise. The web app never builds a Stripe URL or passes a return URL of its own. Each button is a plain form post, so it works without JavaScript; with JavaScript it is disabled and reads "Opening Stripe…" while the action runs (`useFormStatus`), so a double click sends one request. Without JavaScript the browser can still submit twice, which creates two sessions, not two customers or charges. Opening the account menu focuses its first item, `Billing` when it is shown.

## Feature flags

Unfinished work ships dark behind a flag. Both sides evaluate flags through [OpenFeature](https://openfeature.dev), with PostHog as the provider when a key is set and an in-memory list otherwise. Each side has its own variables.

### Web

The web app evaluates every flag on the server, through `@openfeature/server-sdk`, and hands the results to client components. `web/src/lib/flags/` wraps it. Callers import only from there, never from OpenFeature or PostHog.

- **Registry:** every flag is declared in `web/src/lib/flags/registry.ts`. `FLAG_KEYS` lists the keys and `FlagKey` is their union type, so checking a flag that is not registered is a type error. At run time an unregistered flag is off. Add the key there before using it. `accounts` turns on sign-in (see [Sign-in on the web](#sign-in-on-the-web)). `agency-pages` turns on `/agencies/{slug}`, `meeting-pages` turns on `/meetings/{id}`, `meeting-transcripts` turns on the transcript section of the meeting page, and `agency-rss-feeds` turns on the agency page's feed link and autodiscovery (see [Turning the pages on](#turning-the-pages-on)). `billing` turns on `/billing` and its return pages and the account menu's `Billing` link (see [Billing on the web](#billing-on-the-web)). `agency-submissions` turns on `/submit-agency` (see [Agency submissions](#agency-submissions)). The landing page and `/agencies` have no flag. `test-only-flag` exists only for the module's own tests.
- **Server components:** `await isFlagEnabled("flag-key")` from `@/lib/flags/server`.
- **Routes behind a flag:** `await requireFlag("flag-key")` from `@/lib/flags/server` calls Next's `notFound()` when the flag is off, so a dark route answers 404 exactly like a path that does not exist.
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

## Web pages

The public pages share one shell, one set of primitives and one way of loading data. Follow these conventions in every page.

### Shell

`web/src/app/layout.tsx` renders the skip link, the header (site name linking home, the primary navigation from `NAV_ITEMS` in `web/src/components/site-nav.tsx`, the account menu when sign-in is on), `<main id="main">` and the footer. A page renders only its content: never its own `<main>`, `<header>` for the site, footer or outer width container. The layout sets the width (`max-w-4xl` with side padding) and the vertical padding. `not-found.tsx` and `error.tsx` at the app root cover every route; add a route-level one only when a route needs different words. There is deliberately no `loading.tsx`: it wraps the page in a Suspense boundary, so the response starts with status 200 before the page can call `notFound()`, and every not-found page would answer 200. A future loading state must sit below the point where a page decides it exists.

Titles: the root layout's `metadata.title` is `{ default: "Civic Scanner", template: "%s | Civic Scanner" }`. A page gives only its own part (`title: "Agencies"`, or the agency's name from `generateMetadata`) and the template adds the site name. The landing page sets no title and gets the default. `not-found.tsx` is titled "Page not found".

### Design tokens

`web/src/app/globals.css` defines the colours as Tailwind 4 `@theme` variables and redefines them under `prefers-color-scheme: dark`, so a utility such as `bg-surface` or `text-ink-muted` is right in both schemes. Use these tokens, not Tailwind's palette (`gray-500`, `blue-700`) or hex values:

| Token | Use |
| --- | --- |
| `canvas`, `surface`, `surface-muted` | page background, cards and the header, hover and quiet fills |
| `ink`, `ink-muted` | body text, secondary text |
| `line`, `line-strong` | borders and dividers |
| `accent`, `accent-strong` | links and their hover; `focus` is the focus ring |
| `scheduled-*`, `held-*`, `cancelled-*`, `ai-*`, `error-*` | `-surface`, `-ink` (and `-line`) for status badges, the AI label and error notices |

Base styles give every focusable element a visible `:focus-visible` outline and every element with an `id` a scroll margin, so in-page links (transcript timestamps) do not land under the edge. Two classes cover text: `link` for an inline link, `prose` for long text (70 characters wide, 1.75 line height, spaced paragraphs). Type is the system font stack: `h1` comes from `PageHeader`, `h2` to `h4` from `Section`. Long words (URLs read out in a transcript, long titles) wrap rather than overflow: `body` has `overflow-wrap: break-word`, and text inside a grid or flex cell needs `wrap-anywhere` or `min-w-0` as well, since `break-word` does not shrink a cell's minimum width.

### Primitives

`web/src/components/ui/`, one component per file, imported by path (there is no index file). Each is a server component unless noted and works at phone width.

| Import | Use it for |
| --- | --- |
| `PageHeader` from `@/components/ui/page-header` | the top of every page: the only `h1`, with optional `breadcrumb`, `eyebrow`, `description` and children for a meta line (status badge, date) |
| `Breadcrumb` from `@/components/ui/breadcrumb` | the trail above the title; pass it through `PageHeader`'s `breadcrumb` rather than rendering it yourself |
| `Section` from `@/components/ui/section` | each titled part of a page; a `<section>` labelled by its `h2` (`level={3}` or `level={4}` for nested parts), with optional `id`, `description` and `actions` |
| `LinkList`, `LinkListItem` from `@/components/ui/link-list` | a list of agencies or meetings; with `href` the whole row is one link to a page of this site, without it the row is plain (for when the target page is behind a flag that is off) and its `description` can hold an external link |
| `MeetingStatusBadge` from `@/components/ui/meeting-status-badge` | a meeting's status as `meetingDisplay(status, startsAt, now)` gives it (see [Meeting status on the page](#meeting-status-on-the-page)); `Badge` from `@/components/ui/badge` for other short labels |
| `ExternalLink` from `@/components/ui/external-link` | every link to an agency's site, document or recording; same tab, `rel="external noopener noreferrer"`, a visible arrow and "(leaves Civic Scanner)" for screen readers; a URL that is not `http` or `https` renders as plain text |
| `AiSummary` from `@/components/ui/ai-summary` | any AI-written text (agenda summary, change note, transcript summary): the "AI-generated" label, the text as paragraphs, and a note that the agency's original is the record (pass `note` to link that original) |
| `EmptyState` from `@/components/ui/empty-state` | a list or section that is legitimately empty |
| `Notice`, `UnavailableNotice` from `@/components/ui/notice` | `Notice` for an inline note (`tone="info"`, the default) or error (`tone="error"`, `role="alert"`); `UnavailableNotice what="Meetings are"` when a loader returns unavailable |
| `MeetingTime`, `Offset` from `@/components/ui/date-time` | every date and time a page shows (see below) |

Components used by one route only go in `web/src/app/<route>/_components/`, with their tests beside them. Move one into `web/src/components/ui/` only when a second route needs it.

### Loading data

Pages load API data through the loaders in `web/src/lib/api/load.ts`, never by calling `api` from `@/lib/api/client` directly: `loadAgencies()`, `loadAgency(slug)`, `loadAgencyMeetings(slug, cursor?, limit?)` and `loadMeeting(id)`. Each returns `{ ok: true, data }` or `{ ok: false }`:

- **Not found.** A 404 or 400 from the API, a slug that is not lowercase letters and digits in hyphen-separated groups (at most 100 characters) and a meeting id that is not a lowercase UUID all call Next's `notFound()`, which renders the root `not-found.tsx` with status 404. The last two make no request.
- **Unavailable.** A network error, a timeout (10 seconds per request, `REQUEST_TIMEOUT_MS`), any other non-2xx status, or a response that does not have the shape the pages rely on returns `{ ok: false }` and logs `API request failed: <route>` on the server. The page renders the rest of itself with an `UnavailableNotice` where the data would be; it never throws for this.
- **Shape checks.** `web/src/lib/api/guards.ts` checks each response at run time before a page sees it: required strings are strings, dates parse, lists are lists, and nullable fields are null or the right type. A meeting's `status` may be any string, and a transcript segment only has to be an object, because the pages handle an unknown status and a malformed segment themselves. Anything else that fails the check makes the whole response unavailable rather than half-rendering it.
- **Deadline.** `loadAgencyMeetings` takes an optional `AbortSignal` that is combined with the per-request timeout, so several requests can share one deadline.
- **Caching.** Every loader calls `connection()` and fetches with `cache: "no-store"`, so pages that load data are rendered per request and always show the API's current state. Data changes on its own schedule (agendas, summaries and transcripts arrive at any time), the meeting response can be over Next's 2 MB data-cache limit once a transcript is attached, and in PostHog mode every page is rendered per request already (see [Feature flags](#web)). To cache later, change `settle` in `load.ts`; it is the only place.
- **Deduplication.** The loaders are wrapped in React's `cache`, so `generateMetadata` and the page calling the same loader with the same arguments make one request.

The types pages need are exported from `@/lib/api/client`: `Agency`, `MeetingStatus`, `MeetingSummary`, `MeetingList`, `MeetingDetail`, `Agenda`, `Recording`, `Transcript`, `TranscriptSegment`.

### Dates and times

All meeting dates are shown in `America/Los_Angeles` with the zone abbreviation, whatever the visitor's zone, through `web/src/lib/datetime.ts`. The functions assemble their output from `Intl` parts with plain spaces, so the same instant gives the same string on the server, in the browser and in tests on any machine.

| Function | Example |
| --- | --- |
| `formatMeetingDateTime` (`MeetingTime` default) | `Wednesday, January 20, 2027 at 9:00 AM PST` |
| `formatMeetingDate` (`format="date"`) | `Wednesday, January 20, 2027` |
| `formatMeetingTime` (`format="time"`) | `9:00 AM PST` |
| `formatShortDate` (`format="short-date"`) | `Wed, Jan 20, 2027` |
| `formatShortDateTime` (`format="short-datetime"`) | `Wed, Jan 20, 2027, 9:00 AM PST` |
| `formatOffset` (`Offset`) | `1:02:03` from `3723000` ms, `2:03` under an hour |

A string that is not a date formats as "Date unknown" and is never upcoming; the formatters never throw. Tests run with `TZ=UTC` (set in `vitest.config.ts`), so a formatter that forgot `America/Los_Angeles` fails on any machine.

Render dates with `<MeetingTime value={iso} format="..." />`, which wraps the text in `<time dateTime>`; `<Offset ms={...} />` does the same for transcript offsets with an ISO 8601 duration. `isUpcoming(iso, now)` takes `now` as an argument: read the clock once per render in the page and pass it down, and pass a fixed date in tests.

### Flags

Each new page starts dark. A route behind a flag calls `await requireFlag("agency-pages")` (or `"meeting-pages"`) first in the page and in `generateMetadata`. A section behind a flag checks `await isFlagEnabled("meeting-transcripts")` and renders nothing when it is off. The page flags are already registered; add a new key to `registry.ts` only for a new flag.

### Tests

Every page, `_components` file and helper gets a Vitest test beside it, and coverage of new files stays at 100%. Pages are async server components: render `await Page({ params: Promise.resolve({ slug: "la-metro" }) })` with Testing Library. Stub `fetch` with `vi.stubGlobal`, then `vi.resetModules()` and import the page, since the API client keeps the `fetch` it was created with; mock `next/server` (`connection`) and `next/navigation` (`notFound` throwing a recognisable error); mock `@/lib/flags/server` with an `isFlagEnabled` and a `requireFlag` that read a set of flags the test controls (`requireFlag` calls the mocked `notFound` when its flag is missing); and silence `console.error` in unavailable cases. Only the flags module's own tests and the root layout's set `NEXT_PUBLIC_FEATURE_FLAGS`. Cover found, not found, unavailable, empty, and the flag off. No network, no real clock: pass `now` explicitly. `web/src/lib/api/load.test.ts` and `web/src/app/agencies/[slug]/page.test.tsx` show the pattern.

### Landing and agencies list

`/` (`web/src/app/page.tsx`) explains the service in fixed copy (what it does, what to keep in mind) and lists every agency the API returns. `/agencies` lists the same agencies under its own heading. Neither has a flag. Each agency row shows its name and its website's host name. With `agency-pages` on, the whole row links to `/agencies/{slug}`; with it off, the row is plain and the host name links to the agency's own site. No agencies gives an empty state; an unavailable API gives an `UnavailableNotice` in place of the list, and the rest of the page still renders.

An agency with no meetings yet is marked "Coming soon", with a line above the list saying such agencies are not connected yet; it still links to its page when `agency-pages` is on. `findComingSoon` in `web/src/lib/api/coming-soon.ts` decides this from data: it asks the API for one meeting per agency (`limit=1`), all in parallel under one shared 10-second deadline, so both list pages make at most one extra request per agency. With more than 25 agencies (`MAX_COMING_SOON_LOOKUPS`) it skips the lookups and marks none. A lookup that fails, times out or returns a malformed response leaves that agency unmarked; it never blocks the list or shows the unavailable notice.

### Agency page

`/agencies/{slug}` (`web/src/app/agencies/[slug]/`), behind `agency-pages`. The header has the agency's name, a link to its website and, with `agency-rss-feeds` on, an "RSS feed" link (see [Agency RSS feeds](#agency-rss-feeds)). An unknown slug is a 404; an unavailable agency keeps the page with the title "Agency" and a notice, and the meetings still load.

- **Upcoming and past.** A meeting is upcoming when it is not `held` and starts at or after the moment of the request; everything else is past. So a scheduled or cancelled meeting stays upcoming until its start time, and a meeting the agency already marks `held` is past even if its listed time is later. Upcoming meetings are listed soonest first, past ones most recent first. A cancelled meeting stays in its list with a "Cancelled" badge.
- **Pagination.** The API returns meetings newest first, 50 per page, with a cursor. The first page must show the soonest upcoming meetings, which are at the end of the API's order, so while the last meeting received is still upcoming and there is a next cursor, the page asks for the next API page, at most 4 requests in all (`MAX_FIRST_PAGE_REQUESTS`). All of them share one 10-second deadline, so the page answers in about one timeout however many follow-ups it makes. When the cap is reached with upcoming meetings still to come (an agency with more than 200 future meetings), the upcoming list says that it shows the furthest-ahead meetings and that more, including sooner ones, continue on the next page, and the past list is left out instead of claiming there are no past meetings. If a follow-up request fails or runs out of time, the meetings already loaded are shown with a notice that the list may be incomplete, and "Older meetings" continues from the page that failed. "Older meetings" links to `?cursor=…` and an older page links back to the newest. On an older page the past list is headed "Older meetings", and its upcoming list appears only if upcoming meetings spill onto it. A cursor that is not 1 to 512 letters, digits, `_` or `-`, or that the API rejects, is a 404.
- **States.** No meetings at all on the first page: "Meetings for this agency are not tracked yet." No upcoming or no past meetings: an empty state in that list. Meetings unavailable: a notice in their place.
- **Links.** With `meeting-pages` on, each meeting row links to `/meetings/{id}`; with it off, rows are plain and link to the meeting on the agency's site when the API gives a web address for it. Slugs and ids from the API are passed through `encodeURIComponent` in every internal link (lists, breadcrumb, pagination, feed link).
- **Statuses.** Each row's badge follows [Meeting status on the page](#meeting-status-on-the-page), so a past meeting never shows "Scheduled". Whether a meeting with an unknown status is upcoming is decided by its date alone.

### Meeting status on the page

The API's `status` is `scheduled`, `held` or `cancelled`. Some agencies' sources give no sign that a meeting took place (RCTC never does; SBCTA and VCTC only once a recording link appears; and no agency's meeting is rechecked after it leaves the 30-day look-back), so a meeting can stay `scheduled` after its date. The API keeps reporting it that way; only the pages change how it reads. `meetingDisplay(status, startsAt, now)` in `web/src/components/ui/meeting-status-badge.tsx` is the one place that decides, and both the agency page's rows and the meeting page use it with the clock the page read once:

| API status | Start | Badge | Tone |
| --- | --- | --- | --- |
| `scheduled` | at or after now | "Scheduled" | `scheduled` |
| `scheduled` | before now | "Date passed" | neutral |
| `scheduled` | not a date | "Status unknown" | neutral |
| `held` | any | "Held" | `held` |
| `cancelled` | any | "Cancelled" | `cancelled` |
| anything else | any | the status in sentence case when it is plain lower-case letters and underscores (`postponed` becomes "Postponed"), otherwise "Status unknown" | neutral |

The display rule does not move meetings between the upcoming and past lists; that split is unchanged.

### Meeting page

`/meetings/{id}` (`web/src/app/meetings/[id]/`), behind `meeting-pages`. An id that is not a UUID, or that the API does not know, is a 404. Ids are lowercase: an uppercase or mixed-case form of a valid id is a 404 too, without a request, so each meeting has exactly one URL. The header shows the agency, the title, a status badge, the date and time in Pacific time, and a link to the meeting on the agency's site when its address is a web link (otherwise the link is left out, as is the agency page's "Agency website"). The breadcrumb links the agency's page only when `agency-pages` is on. A cancelled meeting gets a notice saying so, and a meeting shown as "Date passed" (see [Meeting status on the page](#meeting-status-on-the-page)) gets a notice that its date has passed and that Civic Scanner has no confirmation from the agency's site that it took place; its metadata description and its empty recording section say the same, and nothing on the page calls it upcoming or held. An unavailable API gives a generic "Meeting" heading and a notice.

The agenda section shows one of these:

- **No agenda.** "No agenda yet", saying a link and summary will appear when the agency publishes one; for a cancelled meeting, "No agenda", saying none was found before it was cancelled.
- **An agenda.** A prominent link to the agency's original document (plain text if the address is not `http` or `https`), the version number when it is above 1, and when the version was found. Then, when there is a change note, "What changed in version N" with the note, labelled AI-generated. Then the summary, labelled AI-generated with a link to the original, or a note that the summary has not been written yet.
- **A later version without a change note.** The section still says the agency revised the agenda and that no description of what changed is available. A null note on version 2 or later does not mean nothing changed: versions are only created for real changes, and the note is missing while the change is waiting for its note (see "Pending window" under [Agenda changes](#agenda-changes)), when the version was stored while the agenda-changes flag was off, or when it predates notes.

### Transcripts

The meeting page's recording and transcript section (`web/src/app/meetings/[id]/_components/transcript-section.tsx` and `transcript-text.tsx`) is behind `meeting-transcripts`; with it off the section is not rendered at all.

- **No recording.** A scheduled meeting says a recording may follow; a held one says none has been found and that agencies post recordings after a meeting, if at all. A cancelled meeting without a recording has no section.
- **One recording.** A link to the recording on the agency's site, then either a note that the transcript is not ready, or a "Summary" (the AI summary, or a note that it is not written yet) and the "Transcript".
- **Several recordings.** "This meeting has N recordings" with links to `#recording-1`, `#recording-2` and so on, then one sub-section per recording ("Recording 1 of 2"), each as above with its own headings one level down.
- **Layout.** The whole transcript is in the page, with no pagination or lazy loading, so find-in-page and printing see every word. Segments are sorted by start time and grouped into blocks of one minute: a block starts at its first segment and takes every segment that starts within 60 seconds of it. Each block shows its start offset (`1:02:03`, from the start of the recording) as a link to itself, with the id `transcript-{recording}-{seconds}`, so any minute can be linked to. Above the transcript, a "Jump to" list links the first block of each quarter hour; it is left out when the transcript spans under two quarter hours. Speakers are not identified.
- **Guards.** Blank segments are dropped. A segment whose start is negative, not a number or over 24 hours goes, in order, into a final "No usable time" block. A transcript with no text at all says so.
- **Size.** Block markup costs little next to the text: a 3,000-segment transcript's HTML stays under 1.3 times its text (a test checks it). The page as served is larger, because Next also embeds the rendered content in its RSC payload: a 2,500-segment, three-and-a-half-hour transcript came to about 700 KB of HTML, about 100 KB gzipped, with about 1,400 DOM elements.

### Turning the pages on

The landing page and `/agencies` are always on. Everything else is behind web flags (see [Feature flags](#web)): in PostHog mode, create each flag as a boolean flag released to everyone; without PostHog, list them in `NEXT_PUBLIC_FEATURE_FLAGS`, for example `NEXT_PUBLIC_FEATURE_FLAGS=agency-pages,meeting-pages,meeting-transcripts,agency-rss-feeds`, which needs a new build.

| Flag | Turns on |
| --- | --- |
| `agency-pages` | `/agencies/{slug}`, and the links to it from the agency lists and the meeting page's breadcrumb |
| `meeting-pages` | `/meetings/{id}`, and the links to it from the agency page |
| `meeting-transcripts` | the recording and transcript section of the meeting page |
| `billing` | `/billing`, `/billing/success`, `/billing/cancel` and the account menu's `Billing` link; needs `accounts` and sign-in, and the API's `accounts` and `billing` flags (see [Billing on the web](#billing-on-the-web)) |
| `agency-rss-feeds` | the agency page's "RSS feed" link and feed autodiscovery; turn it on together with the API's flag of the same name, and only once `agency-pages` and `meeting-pages` are on (see [Agency RSS feeds](#agency-rss-feeds)) |
| `agency-submissions` | `/submit-agency` and the "Suggest an agency" link on `/agencies`; needs sign-in (`accounts` and WorkOS) and the API's flag of the same name (see [Agency submissions](#agency-submissions)) |

Agency pages link to meeting pages only when both `agency-pages` and `meeting-pages` are on. Each flag can be turned off again at any time: its route answers 404 and the links to it disappear.

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

`internal/llm` has two providers behind one interface. `llm.New(cfg.LLM)` returns an `llm.Client`, whose one method, `Complete`, takes a tier, a system prompt, a user prompt and an optional `MaxOutputTokens`, and returns the text, the model that answered, the finish reason, and the input and output token counts. Callers, the summary jobs included, do not know which provider answers.

- **`gateway`** (the default) calls [Vercel AI Gateway](https://vercel.com/docs/ai-gateway/sdks-and-apis/openai-chat-completions) through its OpenAI-compatible `POST /chat/completions` endpoint, using only the standard library. An empty system prompt is left out of the request, and `MaxOutputTokens` is sent as `max_tokens` only when it is set.
- **`anthropic`** calls the [Anthropic Messages API](https://platform.claude.com/docs/en/api/messages) directly (`POST /v1/messages`) through the official Go SDK, `github.com/anthropics/anthropic-sdk-go/v2`. See [The Anthropic provider](#the-anthropic-provider).

`LLM_PROVIDER` picks one. Callers pick a tier, never a model: config maps each tier to a model ID of the chosen provider, so a model is swapped by changing an environment variable.

| Variable | Default | Meaning |
| --- | --- | --- |
| `LLM_PROVIDER` | `gateway` | `gateway` or `anthropic`, in lower case. Anything else stops the binaries at startup. Unset, a deployment behaves exactly as before this variable existed. |
| `AI_GATEWAY_API_KEY` | none | AI Gateway API key, sent as `Authorization: Bearer`. The LLM client uses it only when `LLM_PROVIDER` is `gateway`; transcription uses it whatever `LLM_PROVIDER` says. A secret: set it in Railway, never in `.railway/railway.ts`. |
| `AI_GATEWAY_BASE_URL` | `https://ai-gateway.vercel.sh/v1` | Gateway base URL, including `/v1`: the client adds only `/chat/completions`. |
| `ANTHROPIC_API_KEY` | none | Anthropic API key. Used only when `LLM_PROVIDER` is `anthropic`. A secret: set it in Railway, never in `.railway/railway.ts`. |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Anthropic API base URL, without `/v1`. The SDK adds `v1/messages` to it, after any path prefix (`https://proxy.example/anthropic` calls `https://proxy.example/anthropic/v1/messages`), so `https://api.anthropic.com/v1` would call `/v1/v1/messages` and fail with a 404. Tests and local runs can point it at a fake. |
| `ANTHROPIC_EFFORT` | `low` | `output_config.effort` sent with every Anthropic request: `low`, `medium`, `high`, `xhigh` or `max`. Ignored by the gateway. |
| `LLM_ROUTINE_MODEL` | `anthropic/claude-haiku-5.5` for `gateway`, `claude-haiku-5-5` for `anthropic` | Model for the `routine` tier: cheap, fast work such as agenda and transcript summaries. |
| `LLM_STRONG_MODEL` | `anthropic/claude-sonnet-5.5` for `gateway`, `claude-sonnet-5-5` for `anthropic` | Model for the `strong` tier: paid directed prompts and other work that needs a stronger model. |
| `LLM_TIMEOUT` | `2m` | Limit on one model call, as a Go duration. Must be positive. |
| `AGENDA_SUMMARY_TIER` | `routine` | Tier the agenda summary job uses (see [Agenda summaries](#agenda-summaries)): `routine` or `strong`. Anything else stops the binaries at startup. |

Every value is checked when the binaries start, never at request time; a bad one stops them with a message naming the variable:

- both base URLs must be `http` or `https` URLs with a host, and `ANTHROPIC_EFFORT` must be one of the five levels, whichever provider is selected;
- with `anthropic`, `ANTHROPIC_BASE_URL` must not end in `/v1` or `/v1/messages` (with or without a trailing slash) and must have no query or fragment; the error names the variable and suggests the URL without the API path. Any other path is a prefix the SDK keeps. With `gateway` the path is not checked, so an `ANTHROPIC_BASE_URL` inherited from a shell or a proxy setup does not stop the binaries;
- a model ID must not contain whitespace;
- with `gateway`, a model ID must be `provider/model` (it contains a `/`), so an Anthropic ID such as `claude-haiku-5-5` left over from a switch is refused;
- with `anthropic`, a model ID must contain neither `/` nor `.`, so a gateway ID such as `anthropic/claude-haiku-5.5`, or a dotted version such as `claude-sonnet-5.5`, is refused. Anthropic IDs are exact strings with no date suffix: `claude-haiku-5-5`, `claude-sonnet-5-5`.

The current gateway model IDs are listed at `https://ai-gateway.vercel.sh/v1/models`.

Without the selected provider's key the binaries start normally: the worker passes no client to the jobs and logs `llm_configured=false`, and a client built anyway answers every `Complete` with `llm.ErrNotConfigured` without a request. The other provider's key is ignored: `LLM_PROVIDER=anthropic` with only `AI_GATEWAY_API_KEY` set is not configured, it does not fall back to the gateway. The worker's `worker started` line logs `llm_provider` and `llm_configured`. No key, prompt or request body is logged.

`Complete` does not retry; River retries the job. Errors tell a worker whether a retry can help, the same way for both providers:

- `llm.IsRetryable(err)` is true for:
  - network failures, the `LLM_TIMEOUT` limit, and a canceled context;
  - 408, 429, and 5xx, including Anthropic's 529 `overloaded_error`;
  - a 200 response that cannot be decoded, has no choices, or has empty text, and, from Anthropic, a stop reason the client does not expect (`tool_use`, `pause_turn` or a new one);
  - from Anthropic, `llm.ErrThinkingBudget`: the reply stopped at `max_tokens` with little text because thinking used the budget (see [The Anthropic provider](#the-anthropic-provider)).

  These are `*llm.Error` values. On a 429 with a `Retry-After` header in seconds, `RetryAfter` holds the delay, which a worker can pass to `river.JobSnooze`.
- `llm.IsAccountFailure(err)` is true for a failure of the account or the configuration rather than of the document: retrying the same request cannot help until someone fixes the key, the plan, the credit or the model ID, and every other request will fail the same way. These `*llm.Error` values have `Account` set and are not retryable:

  | | Gateway | Anthropic |
  | --- | --- | --- |
  | Key missing, invalid or revoked | 401 | 401 `authentication_error` |
  | No credit, billing problem | 402 | 402 `billing_error` |
  | Not allowed (plan or tier excludes the model, region, workspace) | 403 | 403 `permission_error` |
  | Model not found or not available to the account, or a wrong base URL | 404 | 404 `not_found_error` |
  | Spend cap reached | | 429 with `error_code` `enforced_spend_limit_reached` or `contract_spend_limit_reached` |
  | Organization on hold | | 400 with `error_code` `organization_on_hold` |
  | Workspace encryption key disabled, or blocked by the key service's network controls | | 400 with `error_code` `cmek_key_disabled` or `cmek_key_network_blocked` |
  | Claude Code workspace key whose creator left the organization | | 400 with `error_code` `claude_code_key_creator_not_member` |

  Anthropic is classified by status, by error type (an `authentication_error`, `billing_error`, `permission_error` or `not_found_error` on any status) and by the `error.details.error_code` values the SDK (v2.12.0) documents for `Messages.New` that concern the organization, its keys or its configuration rather than the request; any other 429 (a rate or ramp limit) stays retryable, the 503 `cmek_key_unreachable` stays retryable, and the request-level 400 codes (compaction, thread, tool and encoding codes, `user_profile_required`, `claude_code_version_too_old`) stay per item. A 400 recognisable only by its message, such as a data-retention requirement on Fable and Mythos models, is not classified, since the SDK says not to match on message wording. Every `*llm.Error` also carries `Provider`, `Model` (the model ID requested), `Type` and `Code` (the provider's error type and code, from the gateway's OpenAI-style `error.type` and `error.code`; a value over 64 bytes or with control characters reads `unrecognized`), so they can be logged without the response body.
- Every other error is permanent and about the item:
  - other 4xx statuses, among them 400, 413 and 422 (`StatusCode` on `*llm.Error`, with the provider's message in the text, and from Anthropic also its error type and request id);
  - `llm.ErrTruncated`: finish reason `length`, so retrying the same input truncates again. Shorten or split the input, or raise `MaxOutputTokens`;
  - `llm.ErrContentFiltered`: finish reason `content_filter`, which is also how an Anthropic refusal is reported;
  - `llm.ErrResponseTooLarge`: a gateway 200 response over 8 MiB;
  - `llm.ErrNotConfigured`, `llm.ErrUnknownTier`, and an invalid gateway base URL.

  Check for these with `errors.Is`. A worker should wrap them in `river.JobCancel` rather than retry them.

When a reply is truncated, filtered or empty, `Complete` returns the error together with the response, so the model, finish reason and token usage can still be recorded. `llm.CheckReply` applies these reply rules, and the fake uses it too.

A River job's own timeout (one minute unless the worker overrides `Timeout`) also cancels the call, so a worker that calls the model should set a `Timeout` longer than `LLM_TIMEOUT`.

### The Anthropic provider

Each `Complete` is one non-streaming `POST /v1/messages`:

- **Request.** `model` is the tier's model ID; `system` holds the system prompt (left out when empty); `messages` is one user message with the user prompt. There is no assistant prefill and no `temperature`, `top_p`, `top_k`, stop sequences or tools: Claude Haiku 5.5 and Claude Sonnet 5.5 answer non-default sampling parameters and prefill with a 400, and so does the API version the SDK sends for any sampling parameter on any model.
- **Thinking and effort.** `thinking` is left out, so both default models run adaptive thinking, and `output_config.effort` is `ANTHROPIC_EFFORT`, `low` by default. Thinking cannot be turned off the same way on both tiers (`thinking: {type: "disabled"}` is a 400 on Claude Sonnet 5.5, and Claude Haiku 5.5 accepts it only at `high` effort or below), so the client lowers effort instead, which both models take. At `low` the model thinks little, and can skip thinking on simple requests; Claude Haiku 5.5's own default is `medium`. Raise `ANTHROPIC_EFFORT` only if summaries turn out measurably worse at `low`. Thinking blocks come back with empty text (the API's default display) and are never part of the reply: only `text` blocks are, in order.
- **`max_tokens`** is `MaxOutputTokens` plus 8,192 (`llm.AnthropicThinkingHeadroom`), because thinking tokens count against `max_tokens`; without the headroom, thinking could cut a summary off. Anthropic's guidance is not to lowball `max_tokens` for a model that thinks (a cap sized for the answer alone can be used up by thinking, stopping at `max_tokens` before any text) and to default to about 16,000 on non-streaming requests; it publishes no bound on how much a model thinks at `low` effort, and the summary prompts carry up to about 40,000 tokens of document. The headroom was 4,096 and is now 8,192, which keeps every summary request well under 16,000. Only tokens actually generated are billed, so the larger cap costs nothing unless the model uses it; at Claude Haiku 5.5's $0.50 per million output tokens, 4,096 more thinking tokens on a call cost at most $0.002. A request without `MaxOutputTokens` gets 16,000 (`llm.AnthropicDefaultMaxTokens`), since the API requires a value. A summary call can therefore produce at most 9,216 output tokens (1,024 + 8,192), and a part-notes call at most 8,960.
- **Stop reasons are checked before the content is read.** `end_turn` and `stop_sequence` are finish reason `stop`; `max_tokens` and `model_context_window_exceeded` are `length`, which `llm.CheckReply` turns into the permanent `llm.ErrTruncated`, with one exception. On `max_tokens`, the client reads `usage.output_tokens_details.thinking_tokens` (the SDK's `Usage.OutputTokensDetails.ThinkingTokens`, which counts the reasoning tokens inside `output_tokens`). When the visible output, `output_tokens` minus `thinking_tokens`, is less than half of the requested `MaxOutputTokens`, the reply was not a long summary cut short but a budget spent on thinking, and `Complete` returns the retryable `llm.ErrThinkingBudget` (with the token counts in the message), so River retries the job instead of cancelling it for 7 days. When the visible output is at least half, when no `MaxOutputTokens` was requested, or when the response does not report `thinking_tokens`, it stays the permanent `llm.ErrTruncated`. `model_context_window_exceeded` is always `llm.ErrTruncated`. A long thinking run can instead outlast `LLM_TIMEOUT` (2 minutes by default) before reaching `max_tokens`; that surfaces as a retryable timeout (`context deadline exceeded`), not as `llm.ErrThinkingBudget`, and is retried the same way; `refusal`, which comes as a 200 response, is `content_filter` and returns `llm.ErrContentFiltered` with the refusal category in the message. A refusal is never stored as a summary: the job cancels as for any other permanent error. Claude Haiku 5.5 has no server-side refusal fallback, so none is configured.
- **Usage.** Input tokens are `input_tokens` plus cache creation and cache read tokens; output tokens are `output_tokens`, which include thinking. The model is the one the API reports. These fill the same `model`, `finish_reason`, `input_tokens` and `output_tokens` log fields as the gateway's.
- **Errors** are the SDK's typed `*anthropic.Error`, unwrapped with `errors.As` and classified by status code like the gateway's, plus the error type (`Type()`) and `error_code` (`ErrorCode()`) for account failures (above); anything else the SDK returns (a connection error, a timeout, an undecodable body) is retryable.
- **Timeout and retries.** The SDK's request timeout is `LLM_TIMEOUT`, and its own retries are off (`llm.AnthropicMaxRetries`, 0), so one `Complete` is exactly one HTTP request, as with the gateway. River's job retries are the only retries: one job attempt makes at most one request per model call it needs (1 for an agenda summary or a change note, at most 9 for a transcript), and the job timeouts built from `LLM_TIMEOUT` stay correct. With the SDK's default of 2 retries an agenda summary job could make 15 requests instead of 5, and a transcript attempt could outlive its job timeout.
- **Explicit configuration.** The client is built with `option.WithoutEnvironmentDefaults()`, so the SDK reads nothing from the environment itself (`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_PROFILE` and profile files, workload identity, `ANTHROPIC_CUSTOM_HEADERS`); the key and base URL come only from the validated config. The SDK sends the key as `Authorization: Bearer`.
- **No prompt caching.** Each summary request's system prompt names a fresh random delimiter, so no two requests share a cacheable prefix, and the prompts are short. Caching would pay for a cache write on every request and never read one.
- **Cost.** Claude Haiku 5.5 costs $0.10 per million input tokens and $0.50 per million output tokens for prompts of up to 100,000 tokens ($0.50 and $2.50 above that; every summary prompt is below it). The largest agenda summary call (about 25,000 tokens in, at most 9,216 out) costs at most about seven tenths of a cent, and the largest transcript attempt (9 calls, about 332,000 tokens in, at most about 81,000 out) about 7 cents. Thinking is billed as output. Claude Sonnet 5.5, the strong tier, costs $2 and $10. Check Anthropic's pricing page before budgeting on these figures.

**Account and configuration failures in the summary jobs.** `summarize_agenda`, `summarize_transcript` and `describe_agenda_change` treat `llm.IsAccountFailure` differently from every other permanent error, because it says nothing about the document and every other pending item would fail the same way:

- **One loud line per refused call.** The job logs, at `ERROR`, exactly `llm account or configuration failure` (`jobs.LLMAccountFailureMessage`) with `provider`, `model`, `status`, `error_type`, `error_code`, `tier`, the item's id (`agenda_version_id` or `transcript_id`) and `job_id`. The provider's message, the key, and the request and response bodies are never logged. Search or alert on that message: `railway logs --service civic-scanner-worker | grep "llm account or configuration failure"`, or a log alert on the string. It replaces the `agenda not summarized` and `transcript not summarized` warnings for that case.
- **Summaries are snoozed, not cancelled.** The summary job returns `river.JobSnooze(1h)`. River (v0.51.2, `internal/jobexecutor/job_executor.go`, `reportResult`) stores a snoozed job as `scheduled` for one hour later with its attempt count put back (`jobRow.Attempt-1`) and a `snoozes` counter in its metadata, so a snooze never uses up one of the job's attempts and the job is never discarded or cancelled by it; `river.JobSnooze` documents that a job can be snoozed indefinitely. The item therefore never enters the 7-day backoff, and it is summarized by itself within about an hour of the account being fixed: no SQL, no restart.
- **The sweeps leave a snoozed job alone.** `scheduled` is one of the states the summary jobs are unique over, so the hourly sweep's `InsertMany` reports the item as a duplicate (`UniqueSkippedAsDuplicate`) and inserts no second job, and the sweep completes as usual, inserting jobs for the other items it lists. A snoozed item still counts towards the sweep's limit (50 agenda versions, 10 transcripts an hour), which keeps the sweep's hourly bound on new work while the account is broken.
- **A circuit breaker stops the other items calling.** `jobs.NewClient` wraps the LLM client once for the three jobs, with one breaker per provider and model ID (the clients report both through `llm.Router`; a client that does not, such as the test fake, is keyed by tier). After an account failure, every call to that model in that worker process is refused without a request for 10 minutes (`llmAccountCooldown`, timed by the jobs' clock); calls to another model go on as usual, so a typo in `LLM_STRONG_MODEL` or a plan that excludes one model does not stop work on the other. A summary job that is refused snoozes for the rest of the cool-down (at least a minute) and logs `llm call skipped after an account or configuration failure` at `info`. After the cool-down one call at a time is let through as a probe: an account failure closes it again for 10 minutes, and any other outcome of that probe (success, a per-item error, a timeout) reopens calls to the model. Each admitted call carries the breaker's generation: a late result from a call admitted before the latest account failure, or from an earlier probe, never reopens calls or ends the probe in flight, while a late account failure always closes the breaker again. A probe that never reports is released after another cool-down. The breakers are in memory: they start closed on every restart, and each worker replica has its own for each model, so with several replicas each one discovers the failure, and probes, by itself.
- **Change notes do not wait.** `describe_agenda_change` never cancels for a model error: on an account failure it logs the same `ERROR` line, trips the breaker for its model, and records the change at once with the word-diff note (reason `llm_account_failure` in `agenda change recorded`); while the breaker for its own model (the `AGENDA_SUMMARY_TIER` model) is open it uses the word diff without calling the model (reason `llm_paused`). A breaker open for another model does not affect it. The `agenda_changed` event is never held back by the account.
- **Worst case while the account stays broken.** Each item makes at most one call an hour (it snoozes for an hour after its own failed call, and a refusal by the breaker costs no call), so at most 24 calls per item per day, all refused (a refused 4xx is not billed). Per worker replica and per broken model, the breaker limits calls to one probe per 10 minutes, 144 a day, plus at most one call per running summary job when the failure is first seen (10 workers on the main queue). There is no cap on how long an item keeps snoozing: until the account is fixed or the flag is turned off, it is retried hourly at no cost and with the `ERROR` line each time it calls.
- **Log volume while the account stays broken.** Each probe logs one `ERROR` line, so about 144 a day per replica and per broken model. Every other pending item wakes when the cool-down ends, finds the breaker open or a probe out, and snoozes again for up to 10 minutes, logging one `info` skip line each time: about 6 an hour per item. With the sweeps' full backlog pending (50 agenda versions and 10 transcripts on one model), that is about 59 × 6 × 24, roughly 8,500 `info` lines a day per replica, plus the jobs' River bookkeeping and no model calls. New agenda versions and transcripts add to it as they arrive. Alert on the `ERROR` message, not on volume.
- **Per-item failures are unchanged.** 400, 413, 422, a refusal, a real truncation and the other permanent errors still cancel the job, and the 7-day backoff still applies to them.

Tests never call a model. `internal/llm/llmtest` provides `llmtest.Fake`, an `llm.Client` for other packages' tests: `llmtest.Returning(text)` answers every call with `text` and finish reason `stop`, `llmtest.Failing(err)` returns `err`, and a `Respond` function can build a response per request. A zero `Fake` replies `fake reply`. `Calls()` returns the requests received. Like the real clients, the fake rejects an unknown tier, returns a retryable `*llm.Error` for a canceled context, and runs `llm.CheckReply` on the responses `Respond` builds. Both clients' own tests run against an `httptest` server. The Anthropic tests point the SDK at it through the base URL and check the request (path, key, model, `system`, a single user message, effort and `max_tokens`, and no sampling parameters, `thinking` or prefill), usage and model mapping, `max_tokens` and context-window stops, refusals, empty and unexpected replies, every status class with exactly one request even when the server sends `x-should-retry: true`, `Retry-After`, the timeout, cancellation, network failures, and that the SDK's own environment variables are ignored.

### Switching summaries to the Anthropic API

Production's worker sets `LLM_PROVIDER=anthropic` in `.railway/railway.ts` and declares `ANTHROPIC_API_KEY` with `preserve()`. The order of the steps does not matter for the worker: with `LLM_PROVIDER=anthropic` and no key it starts normally and treats the LLM as not configured, so summary jobs complete without a model call, as before.

1. Set `ANTHROPIC_API_KEY` on the `civic-scanner-worker` service in Railway (Variables). Setting it redeploys the worker.
2. Merge the change and approve the `Railway config` apply, which sets `LLM_PROVIDER=anthropic` and keeps the key. The worker's `worker started` line then shows `llm_provider=anthropic llm_configured=true`.
3. Clear the summary jobs that the gateway's 403 cancelled before account failures were snoozed instead, so the next hourly sweep retries them instead of waiting 7 days: run the `DELETE` under [Transcript summaries](#transcript-summaries) ("What a failure costs"). An account failure no longer cancels a job, so this step is needed only once, for those older rows.

To go back to the gateway, set `LLM_PROVIDER` to `gateway` in `.railway/railway.ts` (or remove it) and apply.

## Object storage

`internal/storage` stores the audio track of meeting recordings in a [Railway bucket](https://docs.railway.com/storage-buckets), which speaks the S3 API. `storage.New(cfg.Storage)` returns a `storage.Store` with six methods: `Put` (streams from an `io.Reader`), `Get` (returns a streaming `io.ReadCloser`), `Delete` (a missing key is not an error), `Stat` (key, size, last modified), `ListOlderThan` (keys last modified before a time, for the retention job) and `ListPrefix` (every key under a prefix, for the transcription job's checkpoints). Keys are chosen by the caller. The `download_audio` job is the first caller (see [Recording audio](#recording-audio)). `Put` uploads in 16 MiB multipart parts, so an upload of unknown length buffers at most one part in memory; without a part size, `minio-go` sizes parts for a 5 TiB object and buffers about 537 MiB.

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

The worker checks every live agency once a day for board meetings, records them, stores the text of each published agenda, and downloads the audio track of each held meeting's recording. These are the first links of the pipeline (check agency, process agenda, summarize the agenda, download audio, transcribe, summarize the transcript). Four River jobs in `internal/jobs` do the checks, agendas and audio, behind the `ingest-check-agencies` feature flag; the audio download is also behind its own flag, `ingest-download-audio` (see [Recording audio](#recording-audio)). `summarize_agenda` and its hourly sweep `enqueue_agenda_summaries` summarize agenda versions behind their own flag (see [Agenda summaries](#agenda-summaries)). `describe_agenda_change` and its hourly sweep `enqueue_agenda_changes` tell real agenda changes from formatting noise and record what changed, behind `ingest-agenda-changes` (see [Agenda changes](#agenda-changes)). `transcribe_audio` and its hourly sweep `enqueue_transcriptions` transcribe stored audio behind `ingest-transcribe` (see [Transcripts](#transcripts)). `summarize_transcript` and its hourly sweep `enqueue_transcript_summaries` summarize each transcript and record `transcript_ready` behind `ingest-summarize-transcripts` (see [Transcript summaries](#transcript-summaries)).

| Variable | Default | Meaning |
| --- | --- | --- |
| `INGEST_CHECK_HOUR_UTC` | `13` | Hour of the day, in UTC, when the daily check runs (13:00 UTC is 05:00 in Los Angeles in winter, 06:00 in summer). An integer from 0 to 23; anything else stops the worker at startup. |

### Jobs

- `enqueue_agency_checks` is a River periodic job. It runs every day at `INGEST_CHECK_HOUR_UTC`:00 UTC, and also when the worker starts, so a run missed while the worker was down happens on the next start. Its args carry the slot it belongs to: the latest scheduled time at or before now. It is unique by slot, so a restart, a deploy or a new River leader inserts it at most once per day. It inserts one `check_agency` job per live agency (`store.ListAgencies`); pending and rejected agencies get none.
- `check_agency` checks one agency. It is unique by agency slug, queue and UTC day (River `UniqueOpts` `ByArgs`, `ByQueue`, `ByPeriod` of 24 hours), and the enqueuer sets its scheduled time to the slot, so a second enqueue in the same day is skipped as a duplicate; a check run by hand is not (see [Running a check by hand](#running-a-check-by-hand)). River keeps completed jobs for 24 hours, which is what lets a completed check block a duplicate. The job has a 10 minute timeout, for slow agency sites, and at most 5 attempts.

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
5. In one transaction, it adds the version with `store.AddAgendaVersionTx`, hashing the normalized text with `store.HashContent`. When the text equals the latest version's, nothing is stored, no event is recorded, and the job completes. With `ingest-agenda-changes` on, a text that differs from the latest version's only in formatting refreshes that version in place, with no new version and no event (see [Agenda changes](#agenda-changes)), and the job logs `formatting_only`. When a version is created, the store records the `agenda_published` (version 1) or, with `ingest-agenda-changes` off and no earlier version pending, `agenda_changed` event in that transaction (see [Meeting events](#meeting-events)). Otherwise a later version is stored with its `agenda_changed` event pending and the job inserts `describe_agenda_change` for it in the same transaction, so a version whose event is still to come always has a job that will write it. The job then calls the agenda follow-up hook in the same transaction; an error there, or in the insert of `describe_agenda_change`, rolls back the version and its event.

Errors of `check_agency`:

- Retryable: `sources.Retryable(err)`, which is `fetch.IsRetryable` (network failures, timeouts, 408, 429, 5xx) plus a canceled context or exceeded deadline, including the job's own timeout. The job returns the error and River retries it with backoff. Database and follow-up errors are retried the same way.
- Permanent: everything else, such as a 404 or other 4xx, `fetch.ErrTooLarge`, markup the adapter cannot parse, or `sources.ErrInvalidMeeting`. The job is cancelled with `river.JobCancel`; the next day's run tries again.

One agency's failure never affects another: each agency is its own job and its own transaction.

Errors of `process_agenda`, classified the same way:

- Retryable: `sources.Retryable(err)` for the fetch and for the extraction (a canceled context or the job's own timeout while the PDF extractor runs), `extract.ErrBlocked`, and database and follow-up errors. River retries with backoff; a page that is still blocked after 5 attempts is discarded and the next day's check enqueues it again.
- Permanent, cancelled with `river.JobCancel`: other fetch errors (404 and other 4xx, `fetch.ErrTooLarge`), `extract.ErrUnsupported`, `extract.ErrMalformed`, `extract.ErrNoText`, `extract.ErrLimitExceeded`, `extract.ErrUnavailable` (the PDF extractor binary is missing), and a meeting that no longer exists (`store.ErrNotFound`). The next day's check enqueues the agenda again.

Meeting status: `held` is final. Once a meeting is `held`, the upsert keeps it `held` whatever a source reports later, so a source that misreports a past meeting as `scheduled` cannot undo it. Other changes follow the source: `scheduled` to `cancelled`, `cancelled` back to `scheduled` (reinstated), and either to `held`. The rule lives in the upsert's SQL, so every caller gets it.

### Running a check by hand

After a fix or a config change, an operator can run the check now instead of waiting for the next day's slot. The worker binary has a subcommand that only inserts jobs:

```sh
railway ssh --service civic-scanner-worker -- /app/worker check-agency la-metro
railway ssh --service civic-scanner-worker -- /app/worker check-agency --all
```

It runs inside the worker's container, because Postgres has no public address, and uses the service's own `DATABASE_URL` and `FEATURE_FLAGS`. It connects (with a 10 second connect timeout, or the URL's `connect_timeout` if shorter, so an unreachable database fails fast), validates, inserts one `check_agency` job per agency through a River client that works no jobs, prints each job id and exits. It needs no `ffmpeg`, bucket or API keys, and the running worker picks the jobs up within seconds. `/app/worker --help` and `/app/worker check-agency --help` print the usage and exit 0. Locally, `DATABASE_URL=... FEATURE_FLAGS=ingest-check-agencies api/bin/worker check-agency octa` does the same after `make run-worker` has built the binaries.

Before running it:

- **The worker must be deployed with this command.** An older image's `/app/worker` ignores its arguments, so `railway ssh ... /app/worker check-agency ...` would start a second full worker inside the SSH session instead of enqueueing anything. Check that the deploy containing the command is live first.
- **Wait until a deploy has finished and the previous instance has drained** (`drainingSeconds` is 30 in `.railway/railway.ts`). A worker from before this change that picks up a manual job ignores the `manual` field, runs it as a scheduled check, and skips agendas already processed that day.

What it does:

- **Targets.** A slug checks that agency; `--all` checks every live agency that has a source adapter and prints the live ones it skipped for having none. A slug that is unknown, not live, or live without an adapter exits with status 1 and a message saying why, and enqueues nothing. So does a database that is not migrated, a missing `DATABASE_URL`, or any other argument.
- **Flag.** The command respects `ingest-check-agencies`: when the flag evaluates to off it exits with status 1 and enqueues nothing, because the worker would skip the check anyway. It does not override the flag. The flag provider cannot always tell an off flag from one it failed to evaluate (PostHog unreachable, quota limited, a flag that failed), so the message says "is off or could not be evaluated"; any provider warning is printed to stderr just above it.
- **Output.** One line per agency: `la-metro: enqueued manual check, job 4211`, `octa: not enqueued again, manual check job 4198 is still running`, or `example-transit: skipped, no source adapter`, then a count. The worker logs `agency check triggered manually` with `"trigger":"manual"` when the job starts. The lines the `check_agency` and `process_agenda` workers log themselves carry `"trigger":"manual"` or `"trigger":"scheduled"`; lines from the source adapters' own loggers and River's own error logs (a failed or retried job) do not, so match those by `job_id` or `agency_slug`.
- **Cost.** `--all` re-fetches every agenda in the 120-day window for every agency, the same load as a daily run, including Firecrawl fallbacks for blocked pages.

How it gets past the daily uniqueness:

- **Manual jobs carry `"manual":true` in their args.** `CheckAgencyArgs.Manual` and `ProcessAgendaArgs.Manual` are omitted from the JSON when false, so scheduled jobs encode exactly as before and keep their unique keys. River's unique key includes the args, so a manual job never collides with a scheduled one, in either direction: a manual check on day D does not stop the scheduled check of day D or D+1, and a scheduled check that already ran on day D does not stop the manual one. The command never inserts `enqueue_agency_checks`, so it cannot add or remove a scheduled run.
- **Manual jobs are unique only while waiting or running.** Their unique options are by args and queue, with no period, over the states `available`, `pending`, `retryable`, `running` and `scheduled`. Running the command twice while the first check has not finished prints the waiting job instead of adding a second one. Once that job is completed, cancelled or discarded, the next run inserts a new one, the same day or any other. A manual job left `running` by a worker that crashed or was killed keeps blocking manual runs for that agency until River's rescuer moves it on: the client sets neither `JobTimeout` nor `RescueStuckJobsAfter`, so River's default applies, and a job is rescued once it has been running for an hour (the rescuer looks every 30 seconds). A rescued job with attempts left becomes `retryable`, which still blocks, until it runs again; one with none left is discarded.
- **Agendas.** A manual `check_agency` passes `Manual` to the follow-up hook (`UpsertedMeeting.Manual`), and `EnqueueProcessAgenda` inserts `process_agenda` with `Manual: true` and the same waiting-or-running uniqueness. So every agenda in the window is read again in that run, including one whose job completed or was cancelled earlier the same day. An unchanged agenda stores no version and records no event, as on any day; a changed one gets its version, event and summary job as usual. A manual and a scheduled job reading the same agenda at the same time are safe: `AddAgendaVersionTx` locks the meeting row, so the second one sees the first one's version and stores nothing.
- **Recordings.** `download_audio` is not marked manual and keeps its daily uniqueness. A manual check enqueues a download only where the scheduled one would: with `ingest-download-audio` on, for a held meeting whose recording has no stored audio, is not backing off after a failure, and has no download waiting, running or completed that UTC day. With the flag off it enqueues none. So a manual run never downloads a recording twice.
- **Summaries, change descriptions and transcripts** follow from new agenda versions and stored audio, not from the check, so a manual run adds them only for an agenda that really changed. Their own flags apply as usual.

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

Adapters are registered by agency slug in `cmd/worker/adapters.go`, a map from slug to `sources.Constructor` (`func(sources.Deps) sources.Adapter`). At startup the worker builds the registry with `sources.NewRegistry(deps, adapters)`, which calls each constructor once with `sources.Deps{Fetcher, Clock, Logger}`: plain HTTP with the Firecrawl fallback (the composition `fetch.New(cfg.Fetch)` returns; the worker builds it from its parts so it can also give the Firecrawl fetcher to `process_agenda`), `sources.SystemClock{}` and the worker's logger. `Deps.Log()` returns the logger, or one that discards everything when none is set, as in most tests. Adapters get their dependencies only from `Deps`, never from globals. Registered today: `la-metro` and `octa` (the Legistar adapter below) and `metrolink` (the Granicus adapter below) and `vctc` (the VCTC adapter below) and `rctc` (the RCTC adapter below) and `sbcta` (the PrimeGov adapter below). Every live agency seeded by the migrations now has an adapter; an agency added later without one is skipped until its adapter lands. The worker logs the registered slugs when it starts.

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

### RCTC

`internal/sources/rctc` reads the Riverside County Transportation Commission from its own WordPress site. RCTC has no feed or API for meetings: its [Meetings & Agendas](https://www.rctc.org/meetings-and-agendas/) page holds one table per month, headed `DATE & TIME` and `MEETING`, for the current month and the six before it, with a row per meeting such as `NOVEMBER 10, 2026 – 9:30 AM` and a link named `COMMISSION MEETING`. The adapter reads that page with one GET per check, capped at `rctc.MaxPageBytes` (4 MiB; the page is about 130 KB). The agency's values are one `rctc.Config`:

```go
var RCTC = rctc.Config{
	PageURL:        "https://www.rctc.org/meetings-and-agendas/",
	AgendaDomain:   "rctc.org",
	Boards:         []rctc.Board{{Name: "Commission Meeting", Monthly: true}, {Name: "Special Commission Meeting"}},
	CommitteeWords: []string{"Committee", "Executive", "BI", "WRC", "TAC", "CSTAC", "TPOC"},
	TimeZone:       "America/Los_Angeles",
	StartTime:      "09:30",
}
```

| Field | Meaning |
| --- | --- |
| `PageURL` | The meetings page. It is also every meeting's `PageURL`, since RCTC has no page per meeting. |
| `AgendaDomain` | The only domain an agenda link may point to, itself or a subdomain, in lower case. |
| `Boards` | Meeting names of the full board, matched whole (below). `Monthly` marks the regular meeting, which RCTC holds once a month. |
| `CommitteeWords` | Words that, as a whole word of the agenda link's file name in any case, mark a committee's agenda listed under a board name. |
| `TimeZone` | IANA zone of the agency's wall-clock times. |
| `StartTime` | The Commission's usual start, `15:04`, local, for a row without a readable time. The page says the Commission meets at 9:30 a.m. on the second Wednesday of the month. |

**Recordings are off for RCTC, pending the CIV-63 spike.** The recordings are on RivCo TV (Castus VOD), and no downloadable media URL is known yet, so RCTC meetings carry agendas only: no `RecordingURL`, no audio download. The adapter never contacts Castus, `riversidecountyca.iqm2.com` (linked from the page for past videos) or YouTube, and it ignores the page's "Previous Commission Meeting" player.

**Known limits.** These follow from what the page shows and are accepted for now:

- **A meeting is stored as it was last seen.** A meeting that leaves the page (older months drop off after about seven months) or moves to another month is not updated again: its row keeps its last date, status and agenda. The store has no way yet to retire a meeting a source no longer lists.
- **Upcoming meetings appear only once RCTC adds that month's table.** The page lists nothing past the current month (on 2026-11-19 it ended with November), so the 90-day look-ahead finds December's meeting only when RCTC adds December.
- **Past meetings stay `scheduled`.** The page shows nothing that proves a meeting took place, and a past date alone is not evidence. CIV-63 will need to set `held` once a recording is matched, and to backfill: the daily check only looks 30 days back, so meetings older than that need a one-off run with a longer window to get their recordings and status.
- **The agenda is the full packet.** RCTC publishes one document per meeting, the agenda followed by every staff report (659 pages, 16 MB and 1.5 million characters of text for 2026-11-10). The agenda summary reads only the first 100,000 bytes (`summarize.MaxAgendaInputBytes`): the agenda itself (it ends about 11,000 bytes in, at the first `ADJOURNMENT`) and the first staff reports, not the whole packet.
- **The Firecrawl fallback cannot help.** If rctc.org refuses plain requests, Firecrawl's answer is markdown, not the page's table markup, so the check fails with `ErrMalformed` rather than reading it.

How the adapter reads the page:

- **Tables.** Every table whose header row has a cell starting with `Date` and a cell `Meeting` (letters only, any case) is a meeting table, in whichever column order; each later row gives a date-and-time cell and a meeting cell with its links. Rows with both cells empty (the page pads its tables with them) are ignored. Text is decoded (`&#8211;`, `&amp;`, `&nbsp;`) and runs of whitespace, including `<br>`, are collapsed.
- **Board only.** A row is kept when a board name, in any case, is one of: the whole meeting cell, cleaned as below; the text of one of its links; or the cell's text outside its links. So `COMMISSION MEETING` beside a `VIDEO`, `PRESENTATIONS` or `Updated Presentation` link is still the Commission meeting. The board names are `COMMISSION MEETING` and `SPECIAL COMMISSION MEETING`. On the page recorded on 2026-11-19 the Commission's rows are all `COMMISSION MEETING` with one link, one a month (May, June, and August to November 2026; the Commission does not meet in July). The other 23 rows are the Executive, Budget and Implementation, Western Riverside County Programs and Projects, Technical Advisory, and Citizens and Specialized Transit Advisory committees, dropped at debug level. A kept row whose chosen agenda link's file name contains a committee word (`november2026executive`, `August-BI-Agenda.pdf`) is dropped with a warning, so a committee agenda listed under the Commission's name never becomes a board meeting. Anything else that names the Commission without being a board name (`COMMISSION WORKSHOP`, `JOINT COMMISSION MEETING`) is dropped with a warning, so a renamed board meeting is visible; workshops are not board meetings, as for LA Metro and VCTC. No special meeting was listed on the recorded page, so `SPECIAL COMMISSION MEETING` is the expected name, not a seen one.
- **Names and status markers.** A status marker is removed from the name before matching: a trailing `-CANCELED` (the page's form, as in `EXECUTIVE COMMITTEE-CANCELED`), ` - CANCELLED`, ` (POSTPONED)` or `-RESCHEDULED`, or a leading `CANCELED - `, `POSTPONED:` or `**CANCELLED**`, in any case. A trailing correction marker (` - REVISED AGENDA`, ` (Amended)`, ` - Updated`) is removed too. When the board name came from a link, a marker in the cell's other text (`<a>COMMISSION MEETING</a> - CANCELED`) or a link that is only a marker (`CANCELLED`) counts as well. A marker word inside other text (`COMMISSION MEETING CANCELLATION NOTICE`, a `Cancellation policy` link) does not. `Title` is the matched `Boards` name, such as `Commission Meeting`, not the row's capitals.
- **Status.** `cancelled` when the name carries a cancel or postpone marker, or when the date cell says `CANCELED`, `CANCELLED`, `POSTPONED` or `RESCHEDULED` (`DECEMBER 9, 2026 – CANCELED`, with no time warning); otherwise `scheduled`, also after the meeting (see the limits above). A postponed or rescheduled row is `cancelled` because the status has only `scheduled`, `held` and `cancelled`, and the meeting will not take place at the time the row shows; it keeps its id, so the stored meeting turns `cancelled` instead of staying `scheduled`. When RCTC then gives the new date, by editing the row or with a row that says `(RESCHEDULED FROM …)`, the meeting is `scheduled` again: a `RESCHEDULED` marker on a row whose date cell says where it was moved from does not cancel it.
- **Time.** The date-and-time cell is read in the agency's zone and converted to UTC, so daylight saving time is applied for that date (`MAY 13, 2026 – 9:30 AM` is 16:30 UTC, `NOVEMBER 10, 2026 – 9:30 AM` 17:30 UTC). Dates are `NOVEMBER 10, 2026`, `Nov. 10th 2026` or `11/10/2026`, optionally after a weekday; times are `9:30 AM`, `9:30 a.m.`, `2 PM` or `NOON`, and the first time after the date is the start. A row with no time starts at `StartTime` with an info log; a row with text that is not a time or a status word (`TBD`, or `9:30` without AM or PM) starts at `StartTime` with a warning.
- **Rescheduled dates.** Two forms where the new date is clear are read: text struck through with `<del>`, `<s>` or `<strike>` is ignored (`<del>DECEMBER 9, 2026</del> DECEMBER 16, 2026 – 1:30 PM` is December 16 at 1:30 PM, and so is `DECEMBER 16, 2026 – <s>9:30 AM</s> 1:30 PM`), and a parenthesis or bracket starting `RESCHEDULED FROM`, `MOVED FROM`, `POSTPONED FROM` or `CHANGED FROM` is ignored, so the date outside it counts.
- **Rows without a date.** A board row must state exactly one full date once those forms are removed. A row with none, only a month, two different dates (`DECEMBER 9, 2026 DECEMBER 16, 2026`, or `RESCHEDULED FROM` outside a parenthesis), only a struck date, or an impossible one (`FEBRUARY 30, 2027`) is skipped with a warning; it is never dated from the month tab, the agenda link or a weekday rule. When the agenda link names a month and year (`december2026agenda`) and the date says another one, the adapter logs a warning and keeps the stated date.
- **Ids.** The page has no id for a meeting. `SourceID` is the matched board name in lower case with every run of other characters turned into `-`, then `/`, then the local month for the regular meeting (`commission-meeting/2026-11`) and the local date for a special meeting (`special-commission-meeting/2026-12-16`). RCTC keys the regular meeting by month itself, with one table and one agenda short link (`/november2026agenda`) per month. So the id stays the same when the clerk adds the agenda link, adds a status or correction marker, changes the case, or moves the meeting to another day or time in the same month, and the upsert updates one row. If two regular meetings are listed in one month, the month id goes to the earliest one that is not cancelled (or the earliest, when all are) and the others get their date (`commission-meeting/2026-12-09`), with a warning. So when a postponed meeting's new date is added as a second row in the same month, the stored meeting moves to the new date and stays `scheduled`, and the postponed row is stored as a separate `cancelled` meeting on the old date. Two special meetings on one day are told apart by their start time (`special-commission-meeting/2026-12-16T1400`). The same board at the same start listed twice is one meeting, with a warning; it keeps the first row's link, or the second's when the first has none.
  - A meeting moved to another month, or a regular meeting renamed into a special one, gets a new id, and the old row stays behind as it was last seen (see the limits above). So does a month id when an earlier regular meeting is added to its month.
- **Agenda.** The agenda link is chosen by what it is, among the meeting cell's links:
  - a link is never the agenda when its text or file name names a supporting document: a report, presentation, video, minutes, attachment, exhibit, item, audio or recording, slides, handout, comments, correspondence, memo or stream (`Revised-Staff-Report-Item-9.pdf`, `Updated Presentation`, `Agenda Item 9 Presentation`);
  - a revised one wins: a link whose text or file name says revised, amended or updated and also says agenda (`Revised Agenda`, `AMENDED` pointing to `Amended-December-Commission-Agenda.pdf`);
  - otherwise the first link whose own text is the board name (`COMMISSION MEETING`);
  - otherwise the first link whose own text names an agenda (`Agenda`).
  - Any other link (`MINUTES`, `Click here`, `REVISED` pointing to a staff report) is not the agenda; with no agenda link the meeting is listed without one.
- **Agenda host.** The chosen link is resolved against the page's final URL, with any `#fragment` dropped, and kept only when it is `https`, on `AgendaDomain` or a subdomain of it (`www.rctc.org`, `rctc.org`), with no user name or password and no port other than 443. Anything else (`http://`, another host, `rctc.org.example.com`, `http://169.254.169.254/…`, `localhost`, a protocol-relative link to another host, `mailto:`) is dropped with a warning naming the resolved address without its password, and the meeting is listed without an agenda. This keeps `process_agenda` from fetching anything but the agency's own documents; the fetcher itself does not yet refuse private addresses or redirects to other hosts.
- **Agenda document.** For the Commission the link is the short link RCTC publishes (`https://www.rctc.org/november2026agenda`), which answers 301 with the file (`/wp-content/uploads/2026/11/November-Commission-Agenda.pdf`); `process_agenda` follows the redirect. The short link is kept rather than the file: the adapter does not fetch agendas, and a revised agenda under a new file name is still reached through the same short link. Whether RCTC replaces a revised agenda under the same file name or a new one is not known; either way the daily `process_agenda` stores a new version when the text changes. There is no shorter agenda than the packet on the page, and minutes are in a separate Laserfiche archive that is not read. When a meeting that is not cancelled has no agenda link and starts within 72 hours or has passed, the adapter logs a warning.
- **Window.** Only meetings starting in the window are returned.
- **Logging.** A row's warnings are logged only when its date is in the window, or, without a date, when its agenda link's month overlaps the window (a row with neither counts as in it). Older months stay on the page for half a year, so their problems are logged at debug level and do not repeat every day.
- **Errors.** Fetch errors are wrapped with `%w` and keep their retryability. A single unusable row is skipped, never fatal. `rctc.ErrMalformed`, which is permanent, means the page's shape changed, so an empty result means "no board meetings in the window", never "could not read the page":
  - no meeting table (this covers a Firecrawl fallback answer, which is markdown, and a renamed header);
  - meeting tables that list no meeting;
  - listed meetings and none with a readable date;
  - listed meetings and none of them a board meeting. The page shows seven months and the Commission meets in eleven of twelve, so a page with none means the names changed.

`www.rctc.org/robots.txt` allows everything (`User-agent: *` with an empty `Disallow:`). Requests go through the shared fetcher with `FETCH_USER_AGENT`, as for every adapter.

The fixture in `internal/sources/rctc/testdata`, `rctc_meetings_and_agendas.html`, was recorded on 2026-11-19 with the application's fetcher and trimmed to the meeting tables (the site's header, menus, scripts and the rest of the page removed). It holds the seven monthly tables from May to November 2026: six Commission meetings, active and cancelled committee meetings, padding rows, short links and direct PDF links (`August-BI-Agenda.pdf`). The Commission meeting of 2026-11-10 is the only one in that day's window; the tests also read the page with a whole-year window and over 40 successive daily windows. Upcoming meetings, special, cancelled, postponed and rescheduled Commission meetings, cells with several links, rows without an agenda link, agenda links on other hosts, other date and time forms, daylight saving dates, duplicates and malformed pages use pages built in the tests in the same markup, and `FuzzMeetings` checks that no input panics or gives an invalid result. The integration tests run the production `rctc.RCTC` through `check_agency` (one request, to rctc.org only; the November meeting stored and its `process_agenda` job enqueued for the short link) and a meeting that gets its agenda, moves within its month and is cancelled over three checks, as one row.

### PrimeGov

`internal/sources/primegov` reads any agency that publishes its meetings through [PrimeGov](https://www.primegov.com)'s public portal API. SBCTA (San Bernardino County Transportation Authority) is the only one today: `www.gosbcta.com/board/upcoming-meetings-agenda/` embeds the portal (`portal-sbcta.primegov.com`), whose data comes from the tenant host `sbcta.primegov.com`. One agency is one `primegov.Config`:

```go
var SBCTA = primegov.Config{
	Host:              "sbcta.primegov.com",
	AgendaHost:        "sbcta.primegov.com",
	CommitteeIDs:      []int{1},
	BoardName:         "Board of Directors",
	AgendaTemplates:   []string{"Packet"},
	EvidenceTemplates: []string{"Video Link"},
	CancelTemplates:   []string{"Cancellation Notice"},
	PageURL:           "https://www.gosbcta.com/board/upcoming-meetings-agenda/",
	TimeZone:          "America/Los_Angeles",
	StartTime:         "10:00",
}
```

| Field | Meaning |
| --- | --- |
| `Host` | The tenant's PrimeGov host, with no scheme, port or path. The API answers there; CIV-13 found that the portal host (`portal-sbcta.primegov.com`) answers API calls with 405. |
| `AgendaHost` | The host every agenda URL is built on, always over `https`. It must be a lower-case host name under `primegov.com`, with no scheme, port, path or user name; anything else stops the worker at startup. |
| `CommitteeIDs` | The `committeeId`s of the full board. |
| `BoardName` | The board's meeting title, matched whole after the title is cleaned (below), without regard to case or repeated spaces. |
| `AgendaTemplates` | Document template names that are the agenda, in order of preference. |
| `EvidenceTemplates` | Document template names whose presence shows a past meeting took place. |
| `CancelTemplates` | Document template names that mean the meeting is cancelled. |
| `PageURL` | The agency's public meetings page, the `PageURL` of every meeting. |
| `TimeZone` | IANA zone of the agency's wall-clock times. |
| `StartTime` | The board's usual start, `15:04`, local, for a meeting without a stated time. |

To add a PrimeGov agency, find the tenant host in the portal iframe on its meetings page, read `ListUpcomingMeetings` to find the board's `committeeId` and title and the template names of its documents, add a `Config` next to `SBCTA` in `internal/sources/primegov/agencies.go`, register `primegov.Constructor(cfg)` under the agency's slug in `cmd/worker/adapters.go`, and record both lists as fixtures. `Constructor` panics on an invalid config; `primegov.New` returns the error.

**Recordings are off for SBCTA, pending CIV-64**, so SBCTA meetings carry agendas only: no `RecordingURL`, no audio download. Each past board meeting has a "Video Link" document, a one-page PDF whose link points at an embed-only Vimeo video (`https://vimeo.com/1243471960` for 2026-11-04) that could not be downloaded in CIV-13. The API's `videoUrl` and `swagitId` are null on every meeting. The adapter uses the "Video Link" document only as evidence that the meeting was held, and never fetches it.

How the adapter reads PrimeGov:

- **Lists.** `GET https://{host}/api/v2/PublicPortal/ListUpcomingMeetings`, then `GET .../ListArchivedMeetings?year={year}` for every year, in the agency's zone, from the window's start to the earlier of its end and now: one year on most days, two in January, when the 30-day look-back reaches the previous year, and never more than five. Neither takes another filter, needs authentication or pages. The upcoming list holds every meeting from today on, committees included (88 on 2026-11-19, up to a test meeting in 2030); the archived list holds that year's past meetings (20 for 2026). SBCTA moved to PrimeGov in 2026: CIV-13 found that `year=2025` returns an empty list, and earlier meetings are on SBCTA's previous site, which the adapter does not read. A meeting in both lists (on the day it moves) is one meeting, and the archived entry wins. When one id appears twice, the board meeting is kept over an entry that is not one, so a stray duplicate can never replace a board meeting; otherwise the later entry wins. A duplicate within one list, or one that pits a board meeting against something else, is logged as a warning. Each list is read with a 16 MiB cap (`primegov.MaxListBytes`); the upcoming list is 50 KB.
- **Board only.** A meeting is kept when its `committeeId` is in `CommitteeIDs`, its title, cleaned as below, is `BoardName`, and its title does not contain the word `TEST` or `PLEASE IGNORE` in any case. All are needed. Checked against the 108 meetings of both lists on 2026-11-19: committee 1 (`meetingTypeId` 2) holds the 16 Board of Directors meetings from 2026-09-02 to 2027-12-01 (one of them `(DARK)`) and one other, "TEST Board PLEASE IGNORE" (id 118, 2030-12-25), which the title rule drops. Dropped as well: the "Board of Directors Metro Valley Study Session" (committee 7, and once, id 175 on 2027-05-13, filed under committee 6), a study session that CIV-13 found is not the full board, as LA Metro's and Metrolink's workshops are not, and the committees: General Policy (4), Legislative Policy (5), Transit (6), Mountain/Desert Policy (8), City/County Manager's Technical Advisory (9), and the Public and Specialized Transportation Advisory and Coordination Council (11), plus "Test Gen Policy committee" (id 120). A dropped meeting of the board's committee is logged as a warning, and one of another committee whose title mentions a board at info level, but only when it falls in the window (debug otherwise), so the far-off test meeting does not warn every day.
- **Dark months.** `(DARK)` is SBCTA's mark for a month in which the body does not meet. Following the product owner's decision, a dark month is left out, not shown as a cancelled meeting: a board meeting whose title carries the marker and that has no documents is not returned, with a debug log only. A dark board meeting that has any document (a Packet, a Cancellation Notice, a Video Link, any template) is returned as `cancelled`. The rule is the one function `darkPlaceholder` in `internal/sources/primegov/primegov.go`. Why the documents decide: the adapter keeps no state, and the only case where leaving out is wrong is a meeting that was stored as a normal board meeting before it was retitled `(DARK)`, since it would then vanish from the output and its row would stay `scheduled`. Nothing in the API marks such a meeting: the recorded dark entries (board 151 on 2027-08-04, and the committees and study session of July 2027) have the same fields as their neighbours, an id in the same sequence, `publishDate` null and no documents, so the data cannot tell a dark month planned from the start from one retitled later. Documents are the one sign that a meeting was real, so a dark meeting with documents is reported as `cancelled` and its stored row is corrected. Dark months appear on the calendar about a year ahead, long before the 90-day look-ahead reaches them, so a dark placeholder is normally never stored. The residual case: a board meeting already in the window as a normal meeting with no documents yet, then retitled `(DARK)`, is left out and its stored row stays `scheduled`; fixing that needs the store to retire meetings a source no longer lists, which it cannot do yet (as for Granicus).
- **Titles.** HTML entities are decoded and runs of spaces collapsed. Status markers are removed, in any case: `CANCELLED`, `CANCELED`, `POSTPONED` or `RESCHEDULED` at the start (also followed by `:` or `-`), and those words or `DARK` at the end after a dash, a colon or in parentheses (`- CANCELLED`, `(Canceled)`, `- POSTPONED`, `(RESCHEDULED)`, `(DARK)`). `(DARK)` appears on "Board of Directors (DARK)" on 2027-08-04 and the committees in July 2027; see Dark months above for what happens to such a meeting. `Title` is what remains, such as `Board of Directors` or `Special Board of Directors Meeting`. For matching against `BoardName`, a leading `Special` or `Regular` (also `Special Meeting of the`, `Regular Meeting of the`) and a trailing `Meeting`, `Special Meeting`, `Regular Meeting`, `Special` or `Regular` (also after a dash or in parentheses) are removed as well. Accepted forms of the board's title under committee 1, each with or without the status markers:
  - `Board of Directors` (the only form in the recorded data), `Board of Directors Meeting`
  - `Special Board of Directors`, `Special Board of Directors Meeting`, `Special Meeting of the Board of Directors`, `Board of Directors - Special Meeting`, `Board of Directors (Special Meeting)`
  - `Regular Board of Directors Meeting`, `Regular Meeting of the Board of Directors`

  SBCTA has held no special or renamed board meeting on PrimeGov yet, so these forms are an assumption. Anything that names another body or kind of session stays excluded, even under committee 1: `Board of Directors - Budget Workshop`, `Board of Directors Workshop Meeting`, `Board of Directors Metro Valley Study Session`, `Board of Directors Committee Meeting`, and test entries (`TEST Board PLEASE IGNORE`, `Board of Directors - TEST`). Such a title under committee 1 is dropped with a warning, so a board meeting named in a new way is visible. A cancel word elsewhere in a title (`… Cancelled Items Review`) does not match the board's name, and the meeting is dropped.
- **Ids.** `SourceID` is PrimeGov's meeting `id`, such as `100` for 2026-11-04. A meeting is created once and edited in place: each body's ids run in sequence with the cancelled ones among them (Legislative Policy 80 to 83, with 82 "- CANCELLED"), the documents are added to the same id as they are published, and the "Video Link" is added to it after the meeting. It does not depend on the title, the date or the order of the lists, so a renamed, rescheduled or cancelled meeting keeps its row.
- **Time.** `dateTime` (`2026-11-04T10:00:00`) is the local wall-clock time, with no offset. It is read in the agency's zone and converted to UTC, so daylight saving time is applied for that date (10:00 is 17:00 UTC on 2026-10-07 and 18:00 on 2026-11-04). The `date` (`Nov 04, 2026`) and `time` (`10:00 AM`) fields say the same; when they disagree with `dateTime` the adapter logs a warning and keeps `dateTime`. A meeting whose `time` is empty and whose `dateTime` is at midnight has no stated time and starts at `StartTime`, with an info log. A `dateTime` at midnight with a stated `time` (`…T00:00:00` and `10:00 AM`) starts at the stated time on that date, with a warning, rather than at local midnight; a stated `12:00 AM` keeps midnight. Without a readable `dateTime`, the `date` and `time` fields are used; with no readable date at all the meeting is skipped with a warning, never dated by a guess. Every meeting recorded on 2026-11-19 has all three fields and they agree. Messages about a meeting outside the window are logged at debug level only.
- **Agenda.** Each meeting has a `documentList` of compiled documents, each from a template: for a board meeting "Packet", and later "Support Materials", "Supplemental Items" and "Video Link"; for a cancelled meeting "Cancellation Notice"; on one committee meeting "REVISED ITEMS". There is no agenda-only document: the Packet is the agenda followed by the staff reports and attachments (364 pages, 51 MB for 2026-11-04), and it is the document the portal offers as the meeting's agenda. `AgendaURL` is `https://{host}/Public/CompiledDocument?meetingTemplateId={templateId}&compileOutputType={compileOutputType}` for the Packet, empty until it is published (upcoming meetings have no documents until about a week before). With several Packets, the newest `publishDate` wins, then the highest template id. The URL redirects to a PDF on `pgwest.blob.core.windows.net` whose name carries the compile time (`Meetings/100/Packet_20261029120705111.pdf`) and whose signed link expires after two days, which is why the adapter returns the stable `CompiledDocument` URL. CIV-13 found that a revision is compiled again under the same template id with a new `publishDate`, so the agenda URL stays the same and the daily `process_agenda` run stores the new text as a new version. Supplemental and revised items are separate documents that are not read. When a board meeting that is not cancelled has no agenda and starts within 72 hours or has started, the adapter logs a warning, so a renamed template cannot make agendas vanish silently. PrimeGov's HTML view (`/Portal/Meeting?meetingTemplateId=645`) redirects to "Document Not Found" for SBCTA's packets, which are PDF only.
- **Status.** `cancelled` when the title carries a cancel or `POSTPONED` marker, or a `(DARK)` marker on a meeting with documents (see Dark months above), or the meeting has a "Cancellation Notice". The meetings table has no postponed status, so a postponed meeting with no new date is `cancelled`; when SBCTA sets the new date on the same meeting and drops the marker, the next check moves it and makes it `scheduled` again. A `RESCHEDULED` marker is not a cancellation: the meeting keeps its PrimeGov id and takes its new `dateTime`. Otherwise `held` when the start has passed and there is evidence the meeting took place: a "Video Link" document or a `videoUrl`. Otherwise `scheduled`; a past date or a Packet alone is not evidence. The three board meetings in the archive (2026-09-02, 2026-10-07, 2026-11-04) each have their Video Link, published the same day or the day after.
- **Agenda host.** As for RCTC, an agenda URL can only point at the agency's own PrimeGov host. The adapter never takes a URL from the API: it builds `https://{AgendaHost}/Public/CompiledDocument?...` from the configured host and the document's numeric `templateId` and `compileOutputType`. A document whose ids are not integers does not decode and is skipped, one with a template id of zero or below is ignored, and a document's `link` field is never read, so a malformed or tampered answer cannot point `process_agenda` at another host. Where the URL then redirects (the signed blob on `pgwest.blob.core.windows.net`) is the fetcher's business and is not checked by the adapter.
- **URLs.** `PageURL` is `Config.PageURL`, SBCTA's public meetings page (allowed by `www.gosbcta.com/robots.txt`), the same for every meeting: PrimeGov has no page per meeting for SBCTA, since the portal is one page and the HTML agenda view does not exist for its templates.
- **Errors.** Fetch errors are wrapped with `%w` and keep their retryability, an archived list's included. `primegov.ErrMalformed`, which is permanent, means the API's shape changed:
  - a body that is not a JSON list (a Firecrawl fallback answer, which is markdown, an HTML error page or an error object);
  - lists with meetings of which none decodes;
  - lists of which no meeting has a positive `id`, a `committeeId`, a `title` and a `dateTime` or `date` (renamed fields decode as empty);
  - lists whose meetings all belong to other committees: the upcoming list always holds months of board meetings, so this means the board's committee id changed;
  - board meetings in the window that all lack a readable date.

  A single meeting or document that does not decode or lacks those fields is skipped with a warning, and the other meetings are kept. An empty list, or `null`, is no meetings.

`sbcta.primegov.com/robots.txt` is `User-agent: * / Disallow: /`. Following the decision in CIV-62, the daily check fetches the lists once a day with `FETCH_USER_AGENT`, and `process_agenda` fetches each board packet in the window once a day the same way (see [Fetching](#fetching)).

- **Volume.** With no conditional GET, every packet in the window is downloaded again each day: one or two packets a day of 20 to 51 MB, roughly 1.5 to 3 GB a month, each packet repeated for about 30 days after its meeting until it leaves the look-back. Two cheap ways to skip an unchanged packet are a possible follow-up: the redirect's `Location` path embeds the compile time (`Packet_20261029120705111.pdf`), and the list gives each document a `publishDate`; either one changes when the packet is compiled again.
- **The stored link.** The version's `source_url`, and the API's `documentUrl`, is the stable `CompiledDocument` URL; the signed blob URL it redirects to is never stored or logged (`TestProcessAgendaStoresTheStableURLNotTheRedirectTarget` in `internal/jobs`). The blob is served with `Content-Disposition: attachment`, so the public "original agenda" link downloads the PDF rather than opening it in the browser.
- **Refusals.** A 403 or 429 from PrimeGov makes `fetch.New` fall back to Firecrawl, which answers in markdown. For the JSON lists that is not a JSON list, so the check ends as a permanent `ErrMalformed` rather than a retry; the next day's check tries again.
- **Time limit.** The whole fetch of a packet, the redirect and the body, must finish within `FETCH_TIMEOUT` (30 s by default). A slow download of a 50 MB packet could exceed it; the job then fails as a timeout and River retries it.

The packet of 2026-11-04 (51,448,564 bytes, PDF 1.7, 364 pages) extracts with `cmd/extract` in 8 s at 128 MB peak memory to 844,848 bytes of readable text, with no unreadable or truncated pages and no bytes cut; it starts with the agenda.

Fixtures in `internal/sources/primegov/testdata`, recorded on 2026-11-19:

- `sbcta_upcoming.json`: 20 of the 88 upcoming meetings, unchanged: the board meetings of 2026-12-02 to 2027-04-07 (none with documents yet; 2027-03-03 is in standard time and 2027-04-07 in daylight time), the dark board meeting of 2027-08-04 (left out), study sessions of committee 7 and the one filed under committee 6, a dark committee meeting, the December committee meetings and the two test meetings.
- `sbcta_archived_2026.json`: the whole archived list for 2026: 20 meetings, three of them board meetings with their Packet and Video Link (2026-11-04 also with Support Materials and Supplemental Items; 2026-10-07 in daylight time), and the committees, three of them cancelled with a Cancellation Notice and one with REVISED ITEMS.

No board meeting was upcoming with a packet, cancelled, special or undated that day, so `TestSourceIDsAreStableAcrossDailyRuns` derives earlier states of meeting 100 from the recorded one (on the calendar, packet posted, rescheduled, renamed special, cancelled, held) and checks its `SourceID` and status through them; those cases and the time, title, agenda and error cases use meetings built in the tests. The integration tests run the production `primegov.SBCTA` through `check_agency` (two requests, to the PrimeGov lists only; four meetings stored, 2026-11-04 as `held`, and one `process_agenda` job, for its packet) and check that an edited meeting keeps its row.

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
	Manual       bool
}
```

`check_agency` calls it for every upserted meeting, inside the upsert transaction; an error rolls back the agency's whole check. The default is `jobs.FollowUps{EnqueueProcessAgenda{Queue}, EnqueueDownloadAudio{Queue, Store, Enabled}}`, which runs each in order and stops at the first error. `EnqueueProcessAgenda` inserts `process_agenda` on the client's queue with `river.ClientFromContextSafely[pgx.Tx](ctx)` and `InsertTx`, and does nothing for a meeting without an agenda URL. `EnqueueDownloadAudio` is described under [Recording audio](#recording-audio). `jobs.NoFollowUp` does nothing. Another implementation is set through `jobs.Config.FollowUp`; one that also wants agendas processed or audio downloaded includes those follow-ups itself.

`process_agenda` has the same kind of seam for new agenda versions, `jobs.AgendaFollowUp`:

```go
type AgendaFollowUp interface {
	AgendaVersionAdded(ctx context.Context, tx pgx.Tx, v store.AgendaVersion) error
}
```

It is called once for each version that was created, never for an unchanged agenda, inside the transaction that inserted the version, so whatever it writes commits or rolls back with the version. It is for follow-up jobs: the summary job (CIV-39) is enqueued here. `describe_agenda_change` is not: `process_agenda` inserts it itself, and an insert failure there rolls back the version, because that job is what records the version's event. The hook must not record meeting events: `AddAgendaVersionTx` records `agenda_published` or `agenda_changed` in the same transaction, or leaves `agenda_changed` to `describe_agenda_change`, so an event written here would be a duplicate. The default is `jobs.EnqueueSummarizeAgenda{Queue, Logger}` on the client's queue, which inserts `summarize_agenda` for the new version (see [Agenda summaries](#agenda-summaries)). `jobs.NoAgendaFollowUp` does nothing. Another implementation is set through `jobs.Config.AgendaFollowUp`; one that also wants summaries calls `EnqueueSummarizeAgenda` itself.

### Agenda summaries

`summarize_agenda` writes a plain-language summary on one agenda version, through `internal/llm` on the tier `AGENDA_SUMMARY_TIER` names (default `routine`). Its args are the agenda version id. The prompt lives in one place, `internal/summarize/agenda.go`, and its tests use `llmtest.Fake`. Two things enqueue it: the agenda follow-up hook, once for each new version, and the hourly sweep below, for versions still without a summary.

- **Flag.** `ingest-summarize-agendas`, separate from `ingest-check-agencies`, so agendas keep being stored while summaries are off, and summaries can be turned off alone (cost, a bad model).
- **Off or unconfigured costs nothing.** With the flag off, or with no LLM (the worker passes no client when the key of the provider `LLM_PROVIDER` selects is empty; a client that answers `llm.ErrNotConfigured` is treated the same), the job completes at once without a model call or a failed attempt. The sweep inserts nothing with the flag off or no client; it checks only for a missing client, so a client that answers `llm.ErrNotConfigured` still gets jobs inserted, which then complete the same way. Those versions stay without a summary until the sweep picks them up once the flag is on and the key is set.
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
- **Timeout.** `LLM_TIMEOUT` plus one minute (3 minutes by default), so the model call's own limit fires first.
- **Logging.** Each attempt logs the tier, model, finish reason, input and output tokens, input bytes, text bytes and whether the input was cut; `agenda summary written` says whether the summary came from the model or was copied.

Errors, classified like `process_agenda`:

- Retryable, River retries with backoff: `llm.IsRetryable` (network failures, the `LLM_TIMEOUT` limit, the job's own timeout, 408, 429, 5xx, an undecodable or empty reply, a reply that is empty once cleaned, and `llm.ErrThinkingBudget`), and database errors. A 429's `Retry-After` is not used for snoozing, as in the other jobs; River's backoff applies. After 5 attempts the job is discarded.
- Account or configuration failures (`llm.IsAccountFailure`): snoozed for an hour without using an attempt, never cancelled, with one `ERROR` line per refused call; see [Transcript summaries](#transcript-summaries) ("What a failure costs").
- Permanent, cancelled with `river.JobCancel`: other 4xx, `llm.ErrTruncated`, `llm.ErrContentFiltered`, `llm.ErrResponseTooLarge`, `summarize.ErrEmptyAgenda`, `summarize.ErrNoDelimiter`, and a version that no longer exists (`store.ErrNotFound`).

**The sweep.** `enqueue_agenda_summaries` is a River periodic job that runs every hour and when the worker starts, under the same flag. Its args carry the hour it belongs to and it is unique by args and queue, so a restart or a second leader inserts it once per hour. With the flag off or no LLM it does nothing. Otherwise it asks `store.ListUnsummarizedAgendaVersions` for at most 50 versions and inserts a `summarize_agenda` job for each with `InsertMany`; a version whose job is still waiting or running is skipped as a duplicate. A version qualifies when it is the latest version of its meeting, its summary is null, the meeting belongs to a live agency and starts inside the ingest window (30 days back to 90 ahead, as for `check_agency`), and no `summarize_agenda` job for it was cancelled or discarded in the last 7 days. Later meetings come first (`starts_at DESC`).

- It covers versions created before the job existed, versions created while the flag was off or the key missing, an enqueue that failed, and jobs that failed. There is no other backfill.
- **Backoff for a version that keeps failing.** A cancelled or discarded job leaves a row in `river_job`, and the client keeps those rows for 7 days (`CancelledJobRetentionPeriod` and `DiscardedJobRetentionPeriod`; River's default for cancelled jobs is 24 hours, and the setting applies to every job kind). The sweep skips a version while such a row exists, so a version that always fails is tried at most once a week, with at most 5 attempts each time, and only while its meeting is in the window. This needs no migration: the state is River's own table, matched on the job's `args->>'agenda_version_id'`.

**Cost.** One try of one version is at most 5 model calls, each with at most 100,000 bytes of document plus a fixed prompt in (roughly 25,000 tokens) and 1,024 tokens out. The hook tries each new version once, a copied summary costs nothing, and the sweep adds at most one try a week for a version that failed. Daily spend is bounded by the number of new agenda versions the daily check produces (a handful per agency) and, while a backlog exists, by the sweep's 50 versions an hour. With the Anthropic provider a call can also spend up to 8,192 tokens of thinking on top (see [The Anthropic provider](#the-anthropic-provider)). There is no spend cap in the service: the only hard limit is the provider account's budget or credit, whose exhaustion (402, or Anthropic's spend-cap 429) is an account failure that snoozes the job hourly until it is fixed, and turning off the flag stops all spend at once. How long a failed version waits, and how to retry it sooner, is under [Transcript summaries](#transcript-summaries) ("What a failure costs").

### Agenda changes

`ingest-agenda-changes` decides, for every agenda text that differs from the meeting's latest version, whether the difference is real or only formatting, and for a real one records an `agenda_changed` event carrying a "what changed" note. With the flag off (the default), every new text is a new version with an immediate `agenda_changed` event and no note, as before.

**Bias.** When in doubt, a change is reported. A false "changed" costs subscribers one redundant notification; a missed change silently leaves the agenda stale. Every rule below gives way in that direction.

- **Where noise is filtered.** In `process_agenda`, inside the transaction that would add the version, under the meeting lock: `store.AddAgendaVersionTx` takes `AgendaVersionInput.Equivalent`, which the job sets to `agendadiff.Equivalent(ctx, latest, new)`, and calls it with the latest version's text only when the hashes differ.
- **A formatting-only difference is a quiet update.** When the new text is equivalent to the latest version's, that version is updated in place: its stored text, `content_hash` and source URL become the new rendering's, in one `UPDATE` in the same transaction. Its id, number, `created_at` (the API's `foundAt`), summary and change note stay, since the content is the same. No version is added, no event is recorded, no summary job is enqueued and no model is called; the job logs `formatting_only`. The next fetch of the same rendering then has the same hash and stores nothing, and a flip back to the other rendering is again a quiet update. `extract.Normalize` is unchanged, so stored hashes are what they were. A summary or change job already running for that version is unaffected: both read the text once and write only their own column, and the text they read is equivalent to the new one; `SummaryOfSameContent` matches hashes of other versions, so a refreshed version simply matches on its new hash from then on.
- **The comparison is deterministic.** `internal/agendadiff` turns each text into a sequence of tokens, for comparison only (nothing it produces is stored):
  - HTML entities decoded, ligatures expanded; Markdown images dropped, struck-through text (`~~…~~`) dropped as deleted, autolinks unwrapped, backslash escapes removed, a leading blockquote `>` before a non-digit and a leading `o ` bullet dropped.
  - Markdown links become their text. When both texts carry Markdown links, the link target is kept as well, so a changed Zoom link or staff-report URL is a change; when only one does (the PDF rendering has no targets), targets are ignored on both sides. Plain URLs written in the text always count.
  - Page furniture. A line that is only `Page N`, `Page N of M`, `N of M`, `N / M` or `- N -` is a page marker and is dropped. A line that is only a number (`3`) is a page marker only when the marker just before it is `N-1` or the one just after is `N+1`; otherwise (`2026`, `500`) it is content. `Page N` (capitalised) with or without `of M` is stripped from the start or end of a line only after a separator (`–`, `-`, `|`, `•`, `:`) or when it carries `of M`; `see page 12` and `see the report on Page 12` inside a sentence are content.
  - Running headers and footers. A line next to a page marker (or with a page phrase stripped from it) is a boundary line. A boundary line whose text occurs at least twice as a boundary line is a running line. A running line is dropped only at boundary positions, and only when the other text has the same running line, or the other text has no page markers at all (a Markdown or HTML rendering, which has no page furniture to match). So a header that differs between two paginated texts, such as a rescheduled meeting's date on every page, is a change. A line that is not at a page boundary is never dropped, however often it repeats: an item title in the list and as a heading, a recommendation repeated under each item, a line that went from two copies to one, all count.
  - A line ending in a hyphen after a letter is joined to a next line that starts in lower case (`trans-` / `portation`), up to 4,096 bytes per joined line.
  - Each word is lower-cased and split into letters-and-digits tokens. A hyphen or apostrophe between letters is removed (`high-speed` across a line break matches `high-speed`). A comma between digits is removed only when it is a well-formed thousands separator (`1,250` matches `1250`; `12,50` and `Items 3,4` do not). A decimal point between digits is kept. These symbols are tokens of their own: `$ € £ ¥ ¢ % ‰ § ¶ ± ≤ ≥ ≠ = < >`, a sign `+`, `-` or `−` directly before a number, and parentheses around a number (accounting negatives). Bullets, Markdown and table markup, other punctuation and quote styles fall away.
  - Two texts are equivalent when the token sequences are equal. Order counts: a reordered agenda is a change.
  - Known remaining blind spots: a change confined to a repeated running header in one paginated text compared against an unpaginated rendering, and a bare number that happens to continue a page-number sequence. Two renderings that differ in a way not listed above are reported as a change.
- **The event waits for its note, in order, and is never retracted.** A real change is stored as a new version with `change_event_pending` true (migration `00011`) and no event, and `process_agenda` inserts `describe_agenda_change` for it in the same transaction. The job writes the note, then `store.RecordAgendaChange` does in one transaction a conditional `UPDATE ... SET change_note = COALESCE(change_note, $note), change_event_pending = false WHERE id = $1 AND change_event_pending` and, only when that updated the row, inserts the `agenda_changed` event. So the event is written exactly once, already carries its note, and is never written for a formatting difference. The real-or-noise decision is made once, at insert; the job only describes.
- **Order.** A version's event is never recorded while an earlier version of the same meeting is still pending: `RecordAgendaChange` returns `store.ErrEarlierChangePending`, and the job checks before calling any model and snoozes for 5 minutes (River's snooze does not use up an attempt). After the earliest pending version has waited an hour (`changeOrderWait`), the later version's job records the earlier ones itself, oldest first, with the word-diff note, then its own; the earlier version's own job then finds nothing to do. A version added while the flag is off but while an earlier version is pending is also stored pending (and gets a job, which records it with the word-diff note), so turning the flag off never lets a later event overtake an earlier one. Consumers see `agenda_changed` events in version order by `sequence`.
- **When.** The event's `occurred_at` is when the change was found, the version's `created_at`, not when the note was written; the payload repeats it as `found_at`. `sequence` stays insert order, so an `agenda_changed` can have an `occurred_at` earlier than an event with a lower sequence.
- **What the note says, and where it came from.** The payload's `note_source` is `model` when the model wrote the note and `diff` when it was built from the word differences (`summarize.FallbackChangeNote`); it is left out when a note set earlier by other means was kept. The model is asked only on the first attempt of a version's first `describe_agenda_change` job, with the flag on and an LLM configured; every other case uses the word diff at once: the flag off, no key for the selected LLM provider, any model error (retryable or not), a retry, a job after an earlier one for the version was cancelled or discarded, a version recorded out of order behind a later one, differences too large to compare, and none found. So a version whose job keeps failing after the model call costs one model call, and its event follows promptly and cheaply. The word-diff note is up to five lines such as `Added: “14. Adopt the 2027 board meeting calendar”`, `Removed: “…”`, `Moved: “…”` or `Changed “$1,250,000,” to “$1,500,000,” after “…”`, then `And N more changes.`; `The agency substantially rewrote this agenda.` when the differences could not be located; `The agency published a revised version of this agenda.` when they were too large to compare or none were found.
- **What the model sees.** Only the differences, never the full texts: for each, up to 8 unchanged tokens' worth of words before and after, the removed words and the added words, as `Difference N` blocks. They are agency text and handled like agenda summaries: every instruction is in the system prompt (`internal/summarize/change.go`), the block sits between a `<changes-D>` and a `</changes-D>` line where `D` is a fresh 32-hex-character delimiter the block is checked not to contain, and the reply is cleaned like a summary and capped at 1,500 bytes. The tier is `AGENDA_SUMMARY_TIER`.
- **Limits and cost of the comparison.** Each text is compared only up to 4 MiB (`agendadiff.MaxCompareBytes`); a larger one is treated as different, so it is stored as a new version (with the plain revised-agenda note), never refreshed in place. Canonicalisation is linear and checks the context every 1,024 lines; the word diff is Myers' algorithm after trimming the common head and tail and checks the context every 64 edit steps, reporting a differing middle of more than 50,000 tokens or more than 1,000 edits as one coarse difference. Measured on a laptop: 1.5 MB of hyphen-ended lines in 0.36 s and 3 MB in 0.68 s (equivalence check and diff together); a 3.96 MB, 15,000-item agenda in 0.40 s for the equivalence check alone, which is what runs in the insert transaction under the meeting row lock. A typical agenda of 100 KB takes about 10 ms. The model gets at most 40,000 bytes of differences and returns at most 512 tokens.
- **Jobs.** `describe_agenda_change` (args: the agenda version id) is unique by args and queue over the non-final states, has at most 5 attempts and a timeout of `LLM_TIMEOUT` plus one minute, reads with plain queries, calls the model with no transaction open, and writes with one short transaction. It runs whatever the flag says, because a pending version is owed its event. A version no longer pending completes at once. Database errors are retried; a version that no longer exists cancels the job; a cancelled context is retried.
- **The sweep.** `enqueue_agenda_changes` runs every hour and when the worker starts, whatever the flag says. It inserts `describe_agenda_change` for at most 50 pending versions created more than 10 minutes ago, oldest first, and logs `agenda change still pending its event` at `warn` for each one pending more than 3 hours. A re-enqueued version never calls the model again, so a version that keeps failing costs no model calls, only a retry an hour.
- **Pending window.** Until its event is recorded, a pending version is the latest version and is visible through the API with a `null` change note; its summary job runs as usual.
- **Existing versions.** Nothing is backfilled. Versions created before the flag existed or while it is off already have their `agenda_changed` event from insert time and keep a `null` change note.
- **Cost.** At most one model call per real change, with at most 40,000 bytes of differences plus a fixed prompt in (roughly 10,000 tokens) and 512 tokens out. Formatting-only differences cost nothing. Turning the flag off stops all spend on notes.

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

`AudioFollowUp` is the seam for transcription (CIV-41): `AudioStored(ctx, tx, recording)` runs in the transaction that records the audio, so a job it inserts with `InsertTx` commits or rolls back with it. It is called once per recording, never for audio that was already stored. The default, `jobs.EnqueueTranscribeAudio`, inserts the transcription job (see [Transcripts](#transcripts)); `jobs.NoAudioFollowUp` does nothing. Retention (CIV-42) deletes the object and calls `store.MarkRecordingAudioDeleted`; the job never downloads such a recording again.

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

## Transcripts

`transcribe_audio` reads one recording's stored audio from the bucket, transcribes it through [`internal/transcribe`](#transcription) and saves the timestamped transcript (`store.AddTranscriptTx`). Its args are the recording id. Two things enqueue it: the audio follow-up hook, in the transaction that records the audio, and the hourly sweep below. It runs on the `audio` queue, whose one worker it shares with `download_audio`, so the worker never holds more than one recording's audio and its chunks at a time.

- **Flag.** `ingest-transcribe`, separate from `ingest-download-audio`, so audio keeps being stored while transcription (the most expensive call in the pipeline) is off.
- **Off or unconfigured costs nothing.** With the flag off, the hook inserts nothing and a job that runs anyway completes at once. Without a client (the worker passes none when the selected provider has no key; production has no `AI_GATEWAY_API_KEY` yet) or with a client that answers `transcribe.ErrNotConfigured`, the job completes without a model call or a failed attempt and the sweep does nothing. Those recordings wait for the sweep.
- **Enqueue.** `jobs.EnqueueTranscribeAudio` is the default `jobs.AudioFollowUp`. It inserts the job inside a savepoint of the audio transaction; if the insert fails it rolls back to the savepoint, logs `transcription not enqueued` at `warn` and returns nil, so the audio is still recorded. A transcription must never undo a download: rolling back would leave the uploaded object unrecorded and make `download_audio` retry the Granicus fetch, and the sweep enqueues the recording later anyway. (Agenda changes, by contrast, require their job, because the change event is held back until it runs.)
- **Uniqueness.** By args and queue over the non-final states (`available`, `pending`, `retryable`, `running`, `scheduled`), like `summarize_agenda`: while a job for a recording is waiting, running or retrying, another insert is skipped; once it is final, the sweep may insert a new one.
- **Timeout and attempts.** `TRANSCRIPTION_JOB_TIMEOUT` (2 hours), at most 3 attempts. The stuck-job rescuer keeps its one-hour default: as for downloads, it leaves a running job alone until the job's own timeout has passed (`TestRescuerLeavesARunningDownloadAloneWhileRescuingOtherJobs` checks a 65-minute transcription too).

What the job does:

1. Flag off or no client: completes (above). A missing recording cancels the job.
2. A recording that already has a transcript completes without reading the audio. A recording with no stored audio (deleted by retention, or never stored) completes.
3. It stats the audio object. Larger than `TRANSCRIPTION_MAX_AUDIO_BYTES` is cancelled as `limit` before anything is read. An object that is missing while the recording still names it is cancelled as `missing_audio`; missing because retention deleted it meanwhile (the recording no longer has that key) completes.
4. It streams the object into a new temporary directory `civic-audio-transcribe-*` (removed on every return path; the worker's startup sweep of stale `civic-audio-*` directories covers it), reading at most the stat'd size plus one byte. A read shorter or longer than the stat'd size is retried, never transcribed.
5. It calls `TranscribeWith` with `MaxDuration`, `Concurrency`, `Retries`, a bucket checkpoint (below) and the temporary directory. Chunks are cut by `media.Splitter`, which runs `ffprobe` and `ffmpeg` through `cmd/limit` with the download job's `AUDIO_MAX_MEMORY` and `AUDIO_MAX_BYTES`, at most 5 minutes per chunk, `-protocol_whitelist file`, `-format_whitelist mp3` and `file:` paths for both the probe and the cut (the download job's own probe of its output uses the same), and kills the process group when the context ends.
6. It cleans the transcript (below) and, in one transaction opened only now, inserts it if the recording still has none and calls the transcript follow-up hook. A transcript saved by another run meanwhile is kept and the hook is not called. The save runs under its own 30-second deadline, detached from the job's cancellation, so a deploy that lands after the model calls finished does not throw the result away; it either commits whole or not at all.
7. After the commit it deletes the chunk checkpoints.

No transaction is open during the bucket read or any model call.

**One transcript per recording.** `transcripts` is unique on `recording_id`, and `store.AddTranscriptTx` is `INSERT ... ON CONFLICT (recording_id) DO NOTHING`: a retry, a re-enqueue or a second run never replaces a transcript, so its summary (CIV-49) is never cleared by a repeat transcription. Two runs that race (only possible if a job is rescued while another runs) can both pay for model calls; the second write is dropped. "Current audio" is the object at the recording's `audio_key`. A recording's audio is written once: the key depends only on the recording id, `download_audio` never downloads a recording that has or had audio, and a changed recording URL is a new recording with its own transcript. So a transcript always describes the recording's current audio, and there is no re-transcription; doing one by hand means deleting the transcript row first.

**Retention races.** Audio deleted before the job reads it completes quietly (steps 2 and 3, and a `Get` that finds nothing). A transcript is written only from a complete read (step 4), so audio deleted while the model runs still gets its transcript: the transcript and the link to the original recording outlive the audio by design (CIV-42).

**Checkpoints and retries.** Each chunk's raw result is saved in the bucket as `transcripts/{recording id}/chunks/{fingerprint}/{index}-{start ms}-{length ms}.json`, where the fingerprint hashes a format version, the audio key, size and last-modified time, `TRANSCRIPTION_PROVIDER/TRANSCRIPTION_MODEL` and `TRANSCRIPTION_PROVIDER_OPTIONS`, so anything that changes the model's output starts over; the chunk's start and length are in the name. A River retry, a rescued job after a deploy, or the next week's sweep reuses every chunk that already succeeded, so a transient error near the end of a six-hour meeting costs one chunk, not the meeting. Within one attempt a retryable chunk error is retried in place from a budget of `TRANSCRIPTION_CHUNK_RETRIES` extra calls (5 s, 10 s, 20 s, at most a minute, or the 429's `Retry-After`). A finished chunk is saved under its own 15-second deadline, detached from the job's cancellation, so a paid result is kept when the job's timeout or a deploy lands right after the model call. A checkpoint is checked when read: at most 20,000 segments and 20,000 words, at most 1 MiB per text and 2 MiB in all, a duration and every time between 0 and the chunk's length plus 2 seconds, starts not after ends. An unreadable, invalid or unreachable checkpoint is a miss; a failed save is logged and the chunk is transcribed again on the next attempt. Once the transcript commits, every object under `transcripts/{recording id}/` is deleted (`storage.Store.ListPrefix`), including checkpoints of earlier models or audio. A job that ends cancelled or discarded leaves them for the next try; retention (CIV-42) should delete `transcripts/{recording id}/` with the audio. They are a few kilobytes per chunk.

**Untrusted output.** The transcript is model output from public audio. Each segment's text and speaker go through `summarize.PlainText` (invalid UTF-8, control characters, every Unicode format character and the tag block removed, whitespace collapsed, the same set as agenda summaries) and are joined to one line; empty segments are dropped. Segment text is cut at 4,000 bytes (`jobs.MaxSegmentBytes`) and speakers at 64, on a UTF-8 boundary, ending with `…`. Negative starts become 0 and an end before its start becomes the start, so every segment passes the store's check. The full text is the cleaned segments joined by spaces, or the cleaned stitched text when the model returned no segments. A transcript with no text is never stored: it is cancelled as `empty` and, like every cancelled job, tried again by the sweep a week later. The job cannot tell a model that returned nothing from a recording that is silent throughout, so both are treated as failures rather than stored as an empty transcript the sweep would never revisit. More than 10,000 segments or 1 MiB of text (`jobs.MaxTranscriptSegments`, `jobs.MaxTranscriptBytes`) is cancelled as `too_large` rather than stored; nothing is truncated.

The caps come from the 8-hour audio limit. Segments end at a sentence end, a pause of 1.5 s, a speaker change or 30 s (the word rules above; Whisper's segments are similar), so board-meeting speech at about 150 words a minute gives roughly 600 to 650 segments and 50 KB of text an hour (the review measured about 2,500 segments and 200 KB for a four-hour meeting). Eight hours is therefore about 5,000 segments and 400 KB; the caps are twice that, rounded. A transcript at the cap is the largest part of `GET /v1/meetings/{id}`: each segment adds about 45 bytes of JSON (`{"endMs":28799000,"startMs":28795000,"text":""}`), so one recording at 10,000 segments and 1 MiB of text is about 1.5 MB of response for ordinary text, and at most about 6.5 MB if every character of the text is one that Go's JSON encoder escapes as six bytes (`<`, `>`, `&`). A realistic eight-hour meeting is about 0.65 MB. Markup is not interpreted; anything that shows a transcript must escape it.

**Limits and cost.**

| Variable | Default | Meaning |
| --- | --- | --- |
| `TRANSCRIPTION_JOB_TIMEOUT` | `2h` | The job's River timeout. Must be positive. |
| `TRANSCRIPTION_MAX_DURATION` | `8h` | Longest audio transcribed. Longer audio is cancelled as `limit` after a probe, before any chunk. |
| `TRANSCRIPTION_MAX_AUDIO_BYTES` | `134217728` | Largest stored audio read (128 MiB, about 9.3 hours at 32 kbps). |
| `TRANSCRIPTION_CONCURRENCY` | `3` | Chunks transcribed at once, 1 to 8. |
| `TRANSCRIPTION_CHUNK_RETRIES` | `3` | Extra upload attempts per job attempt, shared by all chunks, 0 to 10. |

Nothing is truncated: audio over a limit fails with kind `limit`. An eight-hour recording is 51 ten-minute chunks (8 h 25 min of audio sent, the 30-second overlaps included); with 3 at a time and a 5-minute limit per request, it fits the 2-hour timeout even if every request takes its full 5 minutes (17 rounds, 85 minutes), and a job that times out resumes from its checkpoints. At the approximate gateway prices in CIV-67 (`elevenlabs/scribe-v2` about $0.22 per audio hour, `microsoft/mai-transcribe-2` about $0.10), a complete transcription costs about $0.93 for a four-hour meeting (26 chunks, 4 h 13 min sent) and at most $1.85 for an eight-hour one with scribe-v2. Worst case per recording: each chunk succeeds once (checkpoints), and each attempt can add at most `TRANSCRIPTION_CHUNK_RETRIES` plus `TRANSCRIPTION_CONCURRENCY` failed calls of at most ten minutes each, so 3 attempts add at most 18 failed calls (3 hours of audio, about $0.66) if the provider bills failed calls at all: about $2.51 per job for an eight-hour recording, plus at most $0.66 a week while the sweep keeps retrying one that fails. Changing `TRANSCRIPTION_MODEL` changes the fingerprint, so a partly transcribed recording starts over with the new model. There is no spend cap in the service; turning off the flag stops all spend.

**Errors.** The job's error says the kind (`transcribe recording {id}: {kind}: ...`), and each failure is logged at `warn` with `kind`.

- Retried by River (`transient`): `transcribe.IsRetryable` (network failures, `TRANSCRIPTION_TIMEOUT`, 402, 408, 429, 5xx, an undecodable reply) once the chunk budget is spent, a canceled context or the job's own timeout, a short bucket read, retryable bucket errors, and database and follow-up errors. After 3 attempts the job is discarded.
- Cancelled with `river.JobCancel`: `limit` (`ErrTooLong`, `ErrAudioTooLarge`, `ErrChunkTooLarge`, `media.ErrLimitExceeded`), `too_large` (transcript or reply over its limit), `empty` (no text), `missing_audio`, `unavailable` (`limit`, `ffmpeg` or `ffprobe` missing, invalid provider options), `malformed` (audio `ffmpeg` cannot read), `refused` (other 4xx from the gateway), and `failed` for anything else; also a missing recording and bucket errors that are not retryable (not configured, access denied).

**The sweep.** `enqueue_transcriptions` is a River periodic job that runs every hour and when the worker starts, unique per hour like the agenda sweeps. With the flag off or no client it does nothing. Otherwise `store.ListUntranscribedRecordings` returns at most 10 recordings that have stored audio and no transcript, belong to a live agency, and have no `transcribe_audio` job cancelled or discarded in the last 7 days, latest meetings first, and it inserts a job for each on the audio queue with `InsertMany`; a recording whose job is still waiting or running is skipped as a duplicate. It covers audio stored before the job existed or while the flag was off or the key missing, an enqueue that failed, and jobs that failed. A recording that keeps failing is tried at most once a week (3 attempts each time), from River's own job table (`args->>'recording_id'`), with no migration. This includes permanent failures: a recording cancelled as `limit`, `too_large`, `empty` or `missing_audio` is tried again every week for as long as its audio is stored, at the cost of a bucket read and a probe; `limit` and `missing_audio` stop before any model call, while `too_large` and `empty` pay for the model again (reusing any checkpointed chunks).

**The follow-up hook.** The job does not summarize and does not record `transcript_ready`; [`summarize_transcript`](#transcript-summaries) does. `jobs.TranscriptFollowUp` is the seam:

```go
type TranscriptFollowUp interface {
	TranscriptSaved(ctx context.Context, tx pgx.Tx, t store.Transcript) error
}
```

It is called once per transcript, only when the transcript was created, inside the transaction that inserts it, so a job it inserts with `InsertTx` commits or rolls back with the transcript; an error rolls the transcript back and the job retries (from its checkpoints). The default is `jobs.EnqueueSummarizeTranscript{Queue, Enabled, Logger}` on the client's main queue, which inserts `summarize_transcript` for the new transcript. `jobs.NoTranscriptFollowUp` does nothing. Another implementation is set through `jobs.Config.Transcription.FollowUp`; one that also wants summaries calls `EnqueueSummarizeTranscript` itself.

**Tests.** The job's integration tests use `transcribetest.Fake`, or the real chunking client against an `httptest` server with `transcribetest.FakeSplitter`, and the in-memory bucket: audio stored through the hook is transcribed once and a re-enqueue changes nothing; the flag off enqueues and calls nothing; no client and an unconfigured client complete quietly; audio deleted before the run, deleted between the stat and the read, and deleted after a complete read; an object missing from the bucket; audio over the size and duration limits; a transcript saved by another run meanwhile; a duplicate insert; a follow-up error rolling the transcript back; hostile model output; a retry after a failed chunk that uploads only that chunk and then removes the checkpoints, with a fake model whose sentences cross both chunk boundaries; text-only chunks stitched into one text; an empty result cancelled and backed off, never stored; bucket reads that fail, break or come back short; a transient failure on the last attempt that the sweep then backs off; and the sweep's choice of recordings, on meetings dated far ahead so other tests' rows in a shared database cannot crowd them out of its limit, with back-off rows only a minute old so River's job cleaner cannot remove them mid-test. The rescuer test checks that a transcription 100 minutes old is left running and one 125 minutes old is rescued. One test runs real `ffmpeg` through `cmd/limit` on a 25-second MP3 in the S3Mock bucket. `internal/transcribe` tests resumption, the shared retry budget, `Retry-After` and its cap, bounded concurrency with identical stitching, stopping after a failure, the duration limit, and each stitching rule; `internal/media` tests the splitter with real `ffmpeg`. Not run against a live gateway: there is no key.

### Transcript summaries

`summarize_transcript` writes a plain-language summary on one transcript, through `internal/llm` on the `routine` tier (`jobs.Config.TranscriptTier`; there is no variable, since the story fixes the tier), and in the same transaction records the meeting's `transcript_ready` event. Its args are the transcript id. The prompts live in `internal/summarize/transcript.go` and their tests use `llmtest.Fake`. Two things enqueue it: the transcript follow-up hook, in the transaction that saves the transcript, and the hourly sweep below. It runs on the main queue, not the audio queue, so it never waits behind a transcription.

- **Flag.** `ingest-summarize-transcripts`, separate from `ingest-transcribe` and `ingest-summarize-agendas`, so transcripts keep being stored while their summaries are off, and summaries can be turned off alone. Off by default, like every flag.
- **Off or unconfigured costs nothing.** With the flag off, the hook inserts nothing, a job that runs anyway completes at once and the sweep inserts nothing. With no LLM (the worker passes none when the selected provider's key is empty) the job completes without a model call or a failed attempt and the sweep does nothing. A client that answers `llm.ErrNotConfigured` is treated the same by the job, but the sweep checks only for a missing client, so it still inserts jobs, which then complete without a model call. Those transcripts stay without a summary, and without `transcript_ready`, until the sweep picks them up once the flag is on and the key is set.
- **Enqueue.** `EnqueueSummarizeTranscript` inserts the job inside a savepoint of the transcript's transaction; if the insert fails it rolls back to the savepoint, logs `transcript summary not enqueued` at `warn` and returns nil, so the transcript still commits. A summary never undoes a transcription.
- **Never twice for one transcript.** The job is unique by args and queue over the non-final states (`available`, `pending`, `retryable`, `running`, `scheduled`), so jobs never stack. A transcript that already has a summary completes without a model call. The write is guarded in the database: `store.RecordTranscriptSummary` is `UPDATE transcripts SET summary = ... WHERE id = $1 AND summary IS NULL AND sha256(full_text) = $hash`, so of two runs that race, the first commit wins and the second updates nothing (Postgres re-checks the `WHERE` on the locked row) and records nothing. Two racing runs (only possible if a job is rescued while another runs) can both pay for model calls; they cannot both write. A transcript whose text changed since it was read (`ErrTranscriptChanged`; only `SaveTranscript`, which no job calls, can change it) is retried, so the next attempt summarizes the new text.
- **One transaction.** `RecordTranscriptSummary` sets the summary and, only when that `UPDATE` changed the row, inserts `transcript_ready` for the recording's meeting in the same transaction. The payload is `{"transcript_id": "<uuid>", "recording_id": "<uuid>"}` (`store.TranscriptEventPayload`). No event without a summary, no summary without its event, and a re-run, a re-enqueue or a second transcription of the same recording (`AddTranscriptTx` keeps the first transcript) records nothing. A meeting split into several recordings gets one `transcript_ready` per recording, when that recording's summary is written: there is no way to know that a meeting's last recording has arrived. Only a transcript replaced with new text by `SaveTranscript` (no production caller) loses its summary and is marked ready again once summarized.
- **No transaction across the calls.** The job reads the transcript with a plain query, calls the model with no transaction open, and writes with one short transaction.
- **Untrusted text.** The transcript is model output from public audio and may contain anything said at the meeting, including instructions aimed at a model. It is handled like agenda text: every instruction is in the system prompt, which says the block is data; the user message is a fixed line written by the job (which part, which times) followed by the text between a line `<transcript-D>` and a line `</transcript-D>`, where `D` is a fresh 32-hex-character delimiter from `crypto/rand`, checked not to occur in the text in any case (8 draws, then `summarize.ErrNoDelimiter`, which cancels the job). The combine call puts the part notes, themselves derived from the transcript, between `<notes-D>` and `</notes-D>` the same way, under unindented `Part N of M` headings written by the job, with every line of the notes indented by two spaces, so a note cannot imitate a heading. The text is sent verbatim, one segment per line, without speaker labels (they change every chunk) or per-segment times. Nothing in it changes the tier, the prompts, the output limits or which row is written. Every reply is cleaned like an agenda summary (format characters and the tag block removed, `extract.Normalize`, a cap at a word boundary ending with `…`): part notes at 3,000 bytes (`summarize.MaxPartNoteBytes`), the summary at 4,000 (`summarize.MaxSummaryBytes`). A reply that is empty once cleaned is retried.
- **Almost no speech.** A transcript of fewer than 20 words (`summarize.MinTranscriptWords`), such as `Thank you.` over a silent feed, is not sent to the model. The job stores `The recording has too little speech to summarize.` (`summarize.NoSpeechSummary`) as its summary and records `transcript_ready` as usual, since the transcript is there to read. An empty transcript is never stored by `transcribe_audio`; one stored by other means cancels the job (`summarize.ErrEmptyTranscript`).
- **Timeout.** `LLM_TIMEOUT` times the most calls a run can make (9) plus one minute: 19 minutes by default.
- **Logging.** Each run logs the tier, model, number of parts and calls, summed input and output tokens, input bytes, text bytes, segments, whether any part was cut and whether the no-speech note was used; `transcript summary written` says whether this run wrote it.

**Long transcripts, in parts.** The model gets at most 160,000 bytes of transcript per call (`summarize.MaxTranscriptPartBytes`, roughly 40,000 tokens), well inside the routine model's context. A transcript of at most 120,000 bytes (`summarize.TranscriptPartTargetBytes`, roughly two and a half hours of speech at the 50 KB an hour measured for transcription) is summarized in one call. A longer one is split into `ceil(bytes / 120,000)` parts, at most 8 (`summarize.MaxTranscriptParts`), of about equal size, each ending on a segment boundary. The splitter does not rely on the transcription job's 4,000-byte segment cap: a longer segment is first broken at word boundaries (else at a UTF-8 boundary) into pieces of at most 4,000 bytes that keep the segment's times, so a part is at most 4,000 bytes over its share and nothing is cut while the transcript fits in 8 parts. Only such a long segment can be split between parts. Each part is sent with its number and its start and end time in the recording, and the model returns notes on that part (at most 768 output tokens). Then one more call combines the notes of every part, in order and labelled with their times, into the summary (at most 1,024 output tokens), so every part of the meeting is represented. A four-hour meeting (about 200 KB) is 2 parts and 3 calls; an eight-hour one (about 400 KB) 4 parts and 5 calls; above 960,000 bytes the parts grow past 120,000 bytes instead of adding a ninth (about 131 KB each at the 1 MiB transcript cap). Only a transcript over about 1.25 MB, which `transcribe_audio` cannot store, would need parts over 160,000 bytes: each part is then cut to its first 160,000 bytes at a line break, so every stretch of the meeting is still represented, the prompts say what was cut, and the summary ends with `Parts of this long meeting were cut short before it was summarized.` (`summarize.TranscriptCutNote`). A transcript without segments (the model returned only text) is split the same way on word boundaries, without times.

- **All or nothing.** Parts are summarized one after another, and the first failure ends the run: nothing is stored, no event is recorded, and no summary is ever built from some of the parts.
- **No checkpoint.** A retry summarizes every part again. Unlike transcription (about a dollar of audio per meeting, which is why `transcribe_audio` checkpoints chunks in the bucket), a part is one routine-tier call of at most about 40,000 input tokens, so what a retry repeats is a few cents; keeping part notes would need either a new column (a migration) or the bucket, which this job otherwise does not use. The job has 3 attempts instead of the agenda summary's 5 to bound the repeat.

**Worst case per transcript.** One attempt makes at most 9 calls (`summarize.MaxTranscriptCalls`): 8 part calls of at most 160,000 bytes of transcript plus a fixed prompt in (about 40,600 tokens) and 768 tokens out, and one combine call of at most 8 × 3,000 bytes of notes plus a fixed prompt in (about 7,000 tokens) and 1,024 tokens out, so at most about 332,000 input and 7,200 output tokens (with the Anthropic provider, up to 8,192 more output tokens of thinking per call, about 81,000 in all; see [The Anthropic provider](#the-anthropic-provider)). A job has at most 3 attempts: 27 calls, about 1,000,000 input and 22,000 output tokens. A transcript whose job is cancelled or discarded is tried again by the sweep at most once a week, at the same cost. A typical four-hour meeting costs 3 calls, about 52,000 input and at most 2,600 output tokens. The hook enqueues each new transcript once and the no-speech note costs nothing. There is no spend cap in the service; turning off the flag stops all spend at once.

Errors, classified like the agenda summary:

- Retryable, River retries with backoff: `llm.IsRetryable` (network failures, the `LLM_TIMEOUT` limit, the job's own timeout, 408, 429, 5xx, an undecodable or empty reply, a reply that is empty once cleaned, `llm.ErrThinkingBudget`), `store.ErrTranscriptChanged`, and database errors. After 3 attempts the job is discarded. The error names the part (`part 2 of 3`) or the combine call that failed.
- Account or configuration failures (`llm.IsAccountFailure`): snoozed for an hour, never cancelled; see below.
- Permanent, cancelled with `river.JobCancel`: other 4xx, `llm.ErrTruncated`, `llm.ErrContentFiltered`, `llm.ErrResponseTooLarge`, `summarize.ErrEmptyTranscript`, `summarize.ErrNoDelimiter`, and a transcript that no longer exists (`store.ErrNotFound`).

**What a failure costs, and how long it waits.** A run stops at its first failed call, so when the provider refuses every call each attempt costs one call.

- **An account or configuration failure** (401, 402, 403, a 404 for the model, a spend cap; the table under [LLM](#llm)) costs one call, logs one `ERROR` line `llm account or configuration failure`, and snoozes the job for an hour without using an attempt; the other summary jobs in the same worker skip their calls to the same model for 10 minutes. The item stays pending and is summarized by itself within about an hour of the account being fixed. Nothing needs to be deleted. Details under [The Anthropic provider](#the-anthropic-provider) ("Account and configuration failures in the summary jobs").
- **A per-item permanent failure** (400, 413, 422, a refusal, a real truncation) costs one call and cancels the job at once.
- **A retryable failure** (429, 5xx, timeouts, a reply cut off by thinking) is retried by River: at most 3 attempts, so up to 3 calls when every attempt fails on its first call, and the job is then discarded.
- After a cancellation or a discard the sweep leaves that transcript alone for 7 days (`summaryFailureBackoff`): it skips a transcript while a cancelled or discarded `summarize_transcript` row for it, finalized in the last 7 days, is in `river_job`, and the client keeps those rows for exactly that long (`CancelledJobRetentionPeriod` and `DiscardedJobRetentionPeriod`). After that the next hourly sweep enqueues it again.
- Agenda summaries behave the same way, with `summarize_agenda`, 5 attempts instead of 3, and only for meetings inside the sweep's window.

To retry cancelled or discarded items sooner (after fixing a per-item cause, or for jobs cancelled by an account failure before this behaviour existed), delete those rows; the unique constraint never blocks a new job (it covers only waiting, running and retrying jobs), so the next hourly sweep re-enqueues every transcript and agenda version still without a summary, up to its limits (10 transcripts and 50 agenda versions an hour). Open a shell on the database service with `railway ssh --service Postgres`, start `psql "$DATABASE_URL"` there, and run:

```sql
DELETE FROM river_job
WHERE kind IN ('summarize_transcript', 'summarize_agenda')
  AND state IN ('cancelled', 'discarded');
```

This removes only the failure history of those two job kinds; River's other jobs and the summaries already written are untouched.

**The sweep.** `enqueue_transcript_summaries` is a River periodic job that runs every hour and when the worker starts, unique per hour like the other sweeps. With the flag off or no LLM it does nothing. Otherwise `store.ListUnsummarizedTranscripts` returns at most 10 transcripts without a summary whose meeting belongs to a live agency, started from 365 days before now to 90 days after, and has no `summarize_transcript` job cancelled or discarded in the last 7 days (River's own table, `args->>'transcript_id'`, no migration), latest meetings first, and it inserts a job for each with `InsertMany`; a transcript whose job is still waiting or running is skipped as a duplicate. It covers transcripts saved before the job existed, while the flag was off or the key missing, an enqueue that failed, and jobs that failed. The look-back covers every transcript the pipeline can produce (audio is downloaded only for meetings in the last 30 days and kept about six months) with room for the flag to stay off for months; an older transcript is summarized only by inserting its job by hand. The query scans the transcripts without a summary; there is no index for it, since that set stays small.

**API and feed.** `GET /v1/meetings/{id}` already returns `recordings[].transcript.summary`; it is `null` until this job writes it, and the transcript's segments are returned before that. The RSS feed shows each `transcript_ready` as a `Transcript ready: ` item (see [Agency RSS feeds](#agency-rss-feeds)); nothing there changed. A meeting with several recordings shows one such item per recording, with the same text, and the item does not include the summary.

**Tests.** `internal/summarize` tests one call for a short transcript, the no-speech and empty cases, a 300 KB transcript split into 3 parts on segment boundaries with every segment in exactly one part and every part's notes in the combine call, the 9-call ceiling at the transcript cap and for an oversized transcript cut in every part, a failed part or combine call that stops the run without a summary, empty and truncated notes, hostile text kept inside its block with the reply cleaned, a transcript without segments, a delimiter that cannot be drawn, part notes capped at 3,000 bytes and indented so a forged `Part N of M` line stays inside its note, and the splitter's properties: on 300 random transcripts (with segments up to 20 KB, blank ones and multi-byte text), single segments of 1 MB, 300 KB between two of 100 KB, and one 600 KB word, every part stays under 160,000 bytes, at most 8 parts, in order, carrying the whole text, and nothing is cut while the text fits in 8 parts. The job's unit tests use a fake store. The integration tests run River against Postgres with `llmtest.Fake` and `transcribetest.Fake`: a transcript saved through the hook is summarized on the routine tier (whatever `AGENDA_SUMMARY_TIER` says), shows in the meeting detail and has one `transcript_ready`, and neither a re-enqueue nor a second transcription calls the model or records a second event; a long transcript whose second part fails stores nothing until the retry, which summarizes all 3 parts and combines them; two recordings of one meeting get one event each; and the sweep does nothing with the flag off, then summarizes an owed transcript, writes the no-speech note without a call, leaves a refused transcript without a summary or event and backs it off, and ignores a meeting older than its look-back; and a transcript commits, with its transcription job succeeding on the first attempt, when the summary job's insert fails in the database (a trigger on `river_job` scoped to the test's queue), which proves the savepoint. `internal/store` tests that eight concurrent writes record one event, that a failing `transcript_ready` insert (a trigger on `meeting_events` scoped to the test's meeting) rolls the summary back, that the hash Postgres computes matches Go's for multi-byte, combining and zero-width text, that a write racing a new text never leaves a stale summary, and the sweep's query.

## End-to-end pipeline test

`internal/jobs/pipeline_integration_test.go` runs the whole pipeline, from an agency check to a transcript summary, through a real River client against Postgres and the S3Mock bucket, with every feature flag on. The test inserts only `check_agency` jobs; every other job is inserted by the pipeline's own follow-up hooks, and the test waits until no job on its queues is waiting or running, then checks that every job completed. It fails at once, listing every job's state, attempt and errors, when a job is retried, cancelled or discarded, or when `process_agenda` completes without recording `agenda_published`.

Faked, at the network edges only:

- The agency's source adapter (`sourcestest.Returning`): one held board meeting with an agenda URL and a recording URL.
- The agency's website: an `httptest` server. It serves the agenda as a PDF built by `extracttest.Lines`, a Granicus-style player page, and a 15-second MP4 that `ffmpeg` generates at the start of the test (no media file is checked in). The player page answers only once the agenda's `agenda_published` event exists, so the two branches record their events in a fixed order.
- The model (`llmtest.Fake`): fixed agenda summaries, change note and transcript summary.
- The transcription provider (`transcribetest.Fake`): a fixed two-segment transcript. It records a hash of the file it is given, which the test compares with the object in the bucket.

Real: the plain fetcher with the Firecrawl fallback as the worker composes it (Firecrawl unconfigured, never called), the `cmd/extract` child process, versioning and change detection in the store, the media resolver, page fetcher and gateway (with the guard opened for loopback, as in the download tests), `cmd/limit`, `ffmpeg` and `ffprobe`, the bucket, every job and hook, and the public API's `GET /v1/meetings/{id}` and `GET /v1/agencies/{slug}/feed.xml` handlers.

What it checks:

1. **First check** (scheduled): the meeting with its fields; one agenda version with the PDF's text and its summary; one recording with an MP3 in the bucket, the transcript, its segments and its summary; the events `agenda_published` then `transcript_ready`, with their payloads; one job of each kind, one call per summary and one transcription; the meeting endpoint with the agenda, the recording, the transcript and both summaries, and the agency's, agenda's and recording's original links; two feed items.
2. **Second check** (manual, with a revised PDF): a second version with its change note (from the model) and its own summary, one `agenda_changed` event after the other two, one `describe_agenda_change` and one more `summarize_agenda`, and nothing else: no second download, transcription, transcript summary or `transcript_ready`, and no second request for the player page or the media. The endpoint and the feed show the revision.
3. **Third check** (manual, nothing changed): the agenda is fetched again and nothing else happens: no version, event, model call, transcription or job besides `check_agency` and `process_agenda`.

The second and third checks are manual (see [Running a check by hand](#running-a-check-by-hand)) because a scheduled `process_agenda` is unique per URL and UTC day of the wall clock, so a second scheduled check in the same test would not read the agenda again.

Before the second check the test deletes the completed `download_audio` job, as River's job cleaner does once its retention has passed. Otherwise that job's daily uniqueness would silently drop a second insert, and a check that enqueued the download again for a recording that already has audio would go unnoticed. With it gone, any such insert shows up as a new `download_audio` job, which the second and third checks rule out.

A second test turns on only `ingest-check-agencies`: the meeting and the agenda text are stored with their `agenda_published` event, and the model, the transcription provider and the recording URL are never called.

The hourly sweeps are not part of it: they scan every agency in the database, and integration tests share one. Each sweep has its own integration tests.

Run it with `make dev-db`, then `cd api && go test -tags=integration -run TestPipeline ./internal/jobs/`. It needs `ffmpeg` and skips the full run without it. It takes about 6 seconds on a laptop.

## Agendas

A meeting's agenda is kept as numbered versions in `agenda_versions` (migration `00004`). Each row belongs to one meeting and holds the source document URL, the extracted text, a content hash, a nullable AI summary, a nullable change note, `change_event_pending` (migration `00011`: true while the version's `agenda_changed` event waits for its note, see [Agenda changes](#agenda-changes)), and `created_at`/`updated_at`. `version` starts at 1 for each meeting and is unique per meeting. The latest version, the highest number, is the one shown. Versions are never deleted. After the insert only the summary, the change note and `change_event_pending` are set, except that with `ingest-agenda-changes` on, a formatting-only difference refreshes the latest version's text, hash and source URL in place (see [Agenda changes](#agenda-changes)).

`internal/store/agendas.go`:

| Function | Does |
| --- | --- |
| `AddAgendaVersion(ctx, in)`, `AddAgendaVersionTx(ctx, tx, in)` | Adds a version from `AgendaVersionInput{MeetingID, SourceURL, Text, ContentHash, Equivalent, DeferChangeEvent}`. When the meeting's latest version has the same hash, it adds nothing and returns that version with `created` false. When `Equivalent` is set and returns true for the latest version's text, it refreshes that version's text, hash and source URL in place and returns it with `created` false and `RefreshedInPlace` true. Otherwise it inserts the next number, records an `agenda_published` (version 1) or `agenda_changed` (later versions) meeting event in the same transaction (see "Meeting events"), and returns `created` true. A later version is inserted with `ChangeEventPending` true and no event instead when `DeferChangeEvent` is set or an earlier version of the meeting is still pending. A missing meeting is `ErrNotFound`. |
| `PreviousAgendaVersion(ctx, v)` | The highest-numbered version below `v`, or `ErrNotFound`. |
| `RecordAgendaChange(ctx, id, note, source)` | For a pending version, in one transaction: sets the change note (keeping one already set), clears `ChangeEventPending` and records `agenda_changed` with `occurred_at` set to the version's `created_at` and the note, `note_source` and `found_at` in its payload. Returns the version and whether this call recorded it; a version that is not pending is returned unchanged with false. `ErrEarlierChangePending` while an earlier version of the meeting is pending. A missing version is `ErrNotFound`. |
| `EarlierPendingAgendaVersions(ctx, v)` | The meeting's pending versions below `v`, oldest first. |
| `AgendaJobFailedBefore(ctx, kind, id)` | Whether a River job of `kind` for the version was cancelled or discarded (read from `river_job`). |
| `ListPendingAgendaChanges(ctx, createdBefore, limit)` | Id and `created_at` of pending versions created before `createdBefore`, oldest first, at most `limit`. |
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

The normalized text is both what is stored as the version's `Text` and what is hashed, so a change only in whitespace, line endings or blank lines is not a new version. The same agenda read once directly and once through Firecrawl's markdown gives different text, so with `ingest-agenda-changes` off a switch between the two paths stores a new version (and records `agenda_changed`); with it on, such a switch is recognised as formatting and refreshes the latest version in place (see [Agenda changes](#agenda-changes)).

#### PDF extraction in a child process

PDFs are read in pure Go with [`github.com/ledongthuc/pdf`](https://github.com/ledongthuc/pdf), so the image needs no `pdftotext`. The library parses the file: cross-reference table, objects, streams and fonts. `internal/extract` runs each page's content stream itself (`content.go`), through the library's exported `Interpret`, because the library's own `Page.Content` fails on real agendas (see [Real-world PDFs](#real-world-pdfs)). That library can loop forever or allocate without bound on a hostile or broken file: a page tree that lists itself in `/Kids`, a `/Count` of two billion, or a deflate bomb of under a megabyte that expands to gigabytes of glyphs. It does this inside its own code, where `recover` and a context cannot reach. So the worker never parses a PDF in its own process.

- **The child.** `cmd/extract` is a separate binary. It reads one PDF on stdin, writes the text on stdout, writes a report line on stderr and exits 0, or writes the reason on stderr and exits 10 (malformed), 11 (a limit) or 12 (too little text). The report line is `extract report: ` followed by a JSON object (see [Partial reads](#partial-reads)). The worker image (`api/Dockerfile.worker`) builds it into `/app`, so it ships as `/app/extract` next to `/app/worker`; `api/Dockerfile` builds every `./cmd/...` binary as before.
- **The parent.** `extract.Subprocess` runs it for each PDF with `exec.CommandContext`. It kills the child when `EXTRACT_PDF_TIMEOUT` passes or when the job's context ends, and reads at most `EXTRACT_MAX_TEXT_BYTES` of output, killing the child if it writes more. A child killed for time or output, or exiting 11, is `extract.ErrLimitExceeded`; a crash or an unknown exit code is `extract.ErrMalformed` with the child's stderr (at most 4 KiB). Exit 0 without a valid report as the last stderr line is `extract.ErrMalformed` too. The job's own cancellation comes back as the context error, which is retried.
- **Memory.** The child polls its own Go runtime memory (`runtime/metrics`: everything mapped, minus what was returned to the OS) every 2 ms and exits 11 once it passes half of `EXTRACT_PDF_MAX_MEMORY`; it also sets the Go soft memory limit to that half so the collector works hard before then. Half, because the library grows slices by doubling, so one allocation can almost double use between two polls; with the trip point at half, peak use stays under the ceiling. Measured on macOS with the defaults: the self-referencing page tree fails in 0.09 s at 11 MB peak RSS, the two-billion `/Count` at once at 11 MB, and a 923 KB deflate bomb (600 MB of content) stops at the 32 MiB page-content limit in 0.02 s at 99 MB (before that limit, the memory ceiling stopped it in 0.16 to 0.33 s at 300 to 380 MB). A real 26-page Berkeley City Council agenda takes 0.25 s at 19 MB. Metrolink's board packets of 2026-11-13 (22.5 MB, 434 pages) and 2026-10-23 (49.2 MB, 443 pages) take 0.37 s at 69 MB and 0.40 s at 131 MB; Legistar agendas of 120 to 170 KB take 0.1 to 0.3 s at 19 MB.
- **Limits inside the child.** The child does not trust the page tree: it walks it itself, at most 32 levels deep and at most 4 × pages + 16 nodes, and rejects a tree whose `/Count` is more than the plausible number of pages, which is the smaller of 2000 and one page per 40 bytes of file. It stops at 200,000 glyphs on a page, at 32 MiB of decoded content on a page (`maxContentBytes`), at 128 MiB of decoded content for the whole document (`maxDocumentContentBytes`, four times the page limit; the largest sample, Metrolink's 49 MB packet, decodes 12 MB), and at `EXTRACT_MAX_TEXT_BYTES` of text, and checks its context between pages. Pages that all point at one content stream count it once per page, so 2000 pages sharing a 32 MiB stream fail on page 5 in under 2 s instead of running into the timeout. A font is decoded once per document, keyed by its dictionary, so 2000 pages sharing a font with a large `ToUnicode` map take 0.1 s instead of 21 s; at most 1 MiB of a `ToUnicode` stream is read. Inherited `/Resources` are looked up at most 33 levels up the `/Parent` chain, so a page that is its own parent does not loop. These catch the cheap cases quickly; the timeout and the memory ceiling catch the rest.
- **Finding the binary.** `EXTRACT_PDF_BINARY` when set; otherwise `extract` in the same directory as the running executable. The worker logs the path at startup and warns if nothing is there; PDF agendas are then cancelled with `extract.ErrUnavailable`. `go run ./cmd/worker` builds the worker into a temporary directory without the extractor, so run the worker locally with `make run-worker`, which builds every binary into `api/bin` and runs `api/bin/worker`, or set `EXTRACT_PDF_BINARY` to a built `extract`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `EXTRACT_PDF_BINARY` | `extract` next to the worker | Path to the `cmd/extract` binary. |
| `EXTRACT_PDF_TIMEOUT` | `60s` | Wall-clock limit on one PDF extraction, after which the child is killed. Must be positive. |
| `EXTRACT_PDF_MAX_MEMORY` | `536870912` | Memory ceiling for the child in bytes (512 MiB). The child exits at half of it. Must be positive. |
| `EXTRACT_MAX_TEXT_BYTES` | `8388608` | Largest text one PDF may produce, in bytes (8 MiB). Must be positive. |

How the text is laid out, and what is not read:

- **Lines.** Glyphs on the same baseline (within half the font size) form a line, and lines run top to bottom. Within a line the glyphs keep the order of the page's content stream; they are not sorted by x position, because the library computes wrong positions for some glyphs, and sorting by them scrambled words on real agendas (on a Berkeley agenda, "To request to speak, use the raise hand function" came out as "To rsuet eqto spkea, use the s re aihd"). A space is inserted where the gap to the previous glyph is wider than 0.15 of the font size, or where the text jumps back more than twice the font size (a new column on the same baseline). When a font gives no usable glyph width, the width is taken as half the font size and the gap must be 0.45 of it. Some words still come out with a stray space where a font's widths are wrong.
- **Not read.** Text inside form XObjects (content drawn with the `Do` operator), which the child's interpreter (`content.go`) ignores; scanned pages without a text layer (there is no OCR); and composite (`Type0`, `Identity-H`) fonts with no `ToUnicode` map, whose codes are glyph numbers. Those come out as U+FFFD, which `Normalize` removes, or as wrong letters: on Metrolink's packets, ligatures (`alterna��ve`, `E�ective`) and some attachment headings (`�$�W�W�D�F�K` for "Attachment"). A document where more than half of the pages give no text is `extract.ErrNoText`, so a mostly empty extraction is never stored as an agenda.

#### Real-world PDFs

The library is strict where agency PDFs are not. The first production run (2026-11-19) read 1 of 7 agendas; every Legistar agenda and both Metrolink packets failed. `internal/extract` now tolerates what PDF readers such as pdf.js and Poppler tolerate, without accepting anything that is not a PDF:

- **Header.** `%PDF-` may start anywhere in the first 1024 bytes; the bytes before it are skipped. The version must be a digit, a dot and a digit with major version 1 or 2, followed by whitespace or `%`. Legistar (LA Metro, OCTA) writes `%PDF-1.7 \n`, with a space before the newline, which the library rejects as "invalid header"; it also rejects PDF 2.0. The child hands the library a reader that shows the first nine bytes as `%PDF-1.7\n`. Nothing is copied or shifted, so every byte offset in the file stays right. Anything else, including an HTML page, a version like `x.y` or `3.0`, or `%PDF-` after the first 1024 bytes, is `extract.ErrMalformed` ("invalid header").
- **Trailer.** Legistar also writes `startxref \n`, which the library does not find ("missing final startxref"); a space or tab around `startxref` in the last 1024 bytes is shown as a newline the same way. Everything after the last `%%EOF` in the last 64 KiB (`endOfFileWindow`) is cut off, whitespace included, because the library looks for `%%EOF` only in the last 100 bytes: 200 trailing spaces gave "missing %%EOF" and 2000 newlines a panic. A file with more than 64 KiB after its last `%%EOF` is `extract.ErrMalformed`. Cutting only whitespace and NUL padding is a clean read; cutting anything else (an appended page, a truncated incremental update) is reported as `trailing_bytes_cut` (see [Partial reads](#partial-reads)), since an update that never got its `%%EOF` is lost and the previous revision is read.
- **Pages split into several content streams.** A page's `/Contents` may be an array of streams, which the spec reads as one stream with a token boundary between parts. The library joins them with no separator, so a part ending in `q` followed by a part starting with the operands of `0.75 0 0 -0.75 ... cm` reads as the token `q0.75`. Metrolink's packets (Granicus attachments) wrap every page as `q`, the page, `Q`, then a footer stream, so most pages lost their `q`, and the library panicked on the next `Q` (`index out of range [-1]`, `page.go:909`) or on a five-operand `cm` (`bad g.Tm`): 166 of 434 and 188 of 443 pages. The child decodes the parts, joins them with a newline, and interprets the result. An operator whose operands sit in the previous part (Metrolink splits `[...]` and `TJ` across parts) keeps them.
- **Content-stream errors.** A `Q` with nothing saved is ignored, and an operator with the wrong number of operands is skipped, instead of failing the page. A compressed stream that ends early (two Metrolink pages have a deflate stream with no end marker) keeps what was decoded. A page whose content still cannot be read counts as a page with no text; when more than 10% of the pages cannot be read, the document is `extract.ErrMalformed` with the first page's error. So a 1-page agenda fails on 1 unreadable page, a 10-page agenda passes with 1 and fails with 2, and a 9-page agenda fails with 1. The share is the product owner's decision (2026-11-19) and is one constant, `maxUnreadablePercent` in `internal/extract/pdf.go`, compared in integer arithmetic (`unreadable × 100 > pages × 10`).
- **Simple fonts with a narrow `ToUnicode` codespace.** Codes in a TrueType or Type 1 font are one byte by definition, but Legistar's fonts declare a `ToUnicode` codespace that covers only part of the codes they map (`<6d> <7a>`, the letters m to z, for a map of 31 characters). The library drops every code outside the codespace, so a header-fixed Legistar agenda read as "n t w y z". For a simple font with no `/Encoding` and a `ToUnicode` stream, the child reads `bfchar` and `bfrange` entries itself and maps each byte, falling back to the library's decoding for bytes the map does not cover. A section it cannot parse (a literal string, bad hex) is ignored. At most the first 1 MiB of a decoded `ToUnicode` stream is read (`maxToUnicodeBytes`); mappings past it fall back to the library's decoding. Composite (`Type0`) fonts still use the library's decoding.
- **Graphics state.** The font's decoding is saved and restored with `q` and `Q` like the font itself; the library kept the last font's decoding after a `Q`, which garbled text on three Metrolink pages.

- **Line breaks after `TJ`.** The library ends every `TJ` with a newline glyph sent through the font's decoding, which gave U+FFFD after every `TJ` in Legistar's fonts (76 to 409 per agenda) and in Metrolink's (thousands). The child appends the newline as text instead. Line layout ignores it either way.

On Metrolink pages the library read without error, the child gives the same glyphs as the library did, except on the three restored-font pages, where the text is now right, and for the `TJ` newlines above; Legistar pages differ only by decoding characters the library dropped. The regression tests (`internal/extract/repair_test.go`) build small PDFs with each of these shapes. Those for the header, the split content streams, the unbalanced state and the narrow codespace check that the library alone still fails on them; the truncated-stream test does not.

#### Partial reads

A document can be read in part: a page that cannot be read counts as empty, and a compressed stream that ends early keeps what was decoded. On success the child reports what it skipped, as the last line of stderr, and `extract.Subprocess` parses it into `extract.Report`:

| Field | Meaning |
| --- | --- |
| `pages` | Pages in the page tree. |
| `unreadable_pages` | Pages whose content failed to parse and gave no text. |
| `truncated_pages` | Pages with a content stream that ended early or failed to decode; the decoded part was read. |
| `first_error` | The first unreadable or truncated page's error, such as `page 192: content stream 1: unexpected EOF` (at most 512 bytes). |
| `decoded_content_bytes` | Decoded content-stream bytes for the whole document. |
| `trailing_bytes_cut` | Bytes cut after the last `%%EOF` when any of them is not whitespace or NUL; 0 when only padding was cut. |

`extract.Extractor.Text` returns it as `Result.PDF` (nil for HTML and text). The agenda job logs it after each PDF: `agenda pdf read` at INFO, or `agenda pdf read in part` at WARN when any page was unreadable or truncated or trailing bytes were cut. It is not stored. On the six agendas of 2026-11-19, only Metrolink's 2026-11-13 packet has any: 2 truncated pages (192 and 201) and none unreadable.

## Meeting events

`meeting_events` (migration `00006`) is the outbox between the pipeline and delivery: RSS feeds and webhooks read what happened to a meeting from here and nowhere else. Each row has a unique `id` (uuid, the public identifier of the event), a `sequence` (`bigint GENERATED ALWAYS AS IDENTITY`, unique), a `type`, the `meeting_id`, a JSON object `payload`, and `occurred_at`: when what the event describes happened. That is `clock_timestamp()` at insert, except for `agenda_changed`, whose `occurred_at` is the version's `created_at`, when the change was found (with `ingest-agenda-changes` on the event is written later, once its note exists). Events are only inserted; nothing updates them. `meeting_id` is a plain foreign key, so a meeting that has events cannot be deleted until its events are, and undelivered events never vanish with their meeting.

| Type | Recorded by | Payload |
| --- | --- | --- |
| `agenda_published` | `AddAgendaVersion` / `AddAgendaVersionTx`, when it creates version 1 | `{"agenda_version_id": "<uuid>", "version": 1}` (`store.AgendaEventPayload`) |
| `agenda_changed` | `AddAgendaVersion` / `AddAgendaVersionTx`, when it creates version 2 or later with `ingest-agenda-changes` off; with it on, `RecordAgendaChange`, once the change note is written (see [Agenda changes](#agenda-changes)) | `{"agenda_version_id": "<uuid>", "version": n, "found_at": "<RFC 3339>"}`, plus `"change_note": "<text>"` and `"note_source": "model"` or `"diff"` when recorded with its note |
| `transcript_ready` | `RecordTranscriptSummary`, when `summarize_transcript` writes a transcript's summary (see [Transcript summaries](#transcript-summaries)); one per recording | `{"transcript_id": "<uuid>", "recording_id": "<uuid>"}` (`store.TranscriptEventPayload`) |

An event is written in the same transaction as the change it describes, so a consumer never sees an event for a change that rolled back, and a committed change always has its event; if the event insert fails, the version is rolled back with it. The one deferred case is `agenda_changed` with `ingest-agenda-changes` on: the version commits marked pending, together with the job that will describe it, and the event is written later, in the transaction that records the change note and clears the mark. It is never written twice and never for a formatting difference. An unchanged agenda adds no version and so no event. The outbox has no deduplication key: every call records a new event. `transcript_ready` is recorded only in the transaction whose conditional `UPDATE` wrote the transcript's summary, so a retry that changes nothing records nothing.

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
| `AddTranscriptTx(ctx, tx, in)` | Inserts `TranscriptInput{RecordingID, Text, Segments}` as the recording's transcript only when it has none, and returns the existing one with `created` false otherwise. The transcription job writes through it. A missing recording is `ErrNotFound`; an invalid segment is `ErrInvalidSegment` before the database is touched. |
| `ListUntranscribedRecordings(ctx, UntranscribedParams{FailedKind, FailedWithin, Limit})` | Recordings with stored audio and no transcript, of live agencies, without a `FailedKind` job cancelled or discarded within `FailedWithin`, latest meetings first. A `Limit` below 1 is `ErrInvalidLimit`. |
| `SaveTranscript(ctx, in)`, `SaveTranscriptTx` | No production caller since the transcription job uses `AddTranscriptTx`; kept for tests and later tools (a deliberate re-transcription would use it). Stores `TranscriptInput{RecordingID, Text, Segments}` as the recording's transcript, replacing any earlier one in place (same id), and says whether it was created. The summary is kept when the text is unchanged and cleared otherwise. Saving the same text and segments again writes nothing and leaves `UpdatedAt` alone. A missing recording is `ErrNotFound`. |
| `GetTranscript(ctx, recordingID)` | The recording's transcript, or `ErrNotFound`. |
| `GetTranscriptByID(ctx, transcriptID)` | One transcript by its own id, or `ErrNotFound`. |
| `RecordTranscriptSummary(ctx, transcriptID, textHash, summary)` | In one transaction, sets the summary only while it is null and `textHash` matches the stored text, and then records `transcript_ready` for the recording's meeting; `recorded` says whether this call did. A transcript that already has a summary is returned unchanged with `recorded` false. A changed text is `ErrTranscriptChanged`, a missing transcript `ErrNotFound`, a zero hash `ErrZeroContentHash`. The summary job writes through it. |
| `ListUnsummarizedTranscripts(ctx, UnsummarizedTranscriptParams{From, To, FailedKind, FailedWithin, Limit})` | Transcripts without a summary whose meeting is of a live agency and starts between `From` and `To`, without a `FailedKind` job cancelled or discarded within `FailedWithin`, latest meetings first. A `Limit` below 1 is `ErrInvalidLimit`. |
| `SetTranscriptSummary(ctx, transcriptID, textHash, summary)`, `SetTranscriptSummaryTx` | Sets the AI summary and returns the updated transcript, only if `textHash` (`store.HashContent` of the text that was summarised) still matches the transcript's text. If the transcript was saved with other text meanwhile, nothing changes and the error is `ErrTranscriptChanged`. A zero hash is `ErrZeroContentHash`. A missing transcript is `ErrNotFound`. |

Like `AddAgendaVersion`, the upsert and save check the parent row first (`SELECT ... FOR KEY SHARE`, which also keeps it from being deleted until the transaction ends) instead of relying on the foreign key. So `ErrNotFound` from `UpsertRecordingTx` or `SaveTranscriptTx` leaves the caller's transaction usable.

The summary check hashes the stored text in Postgres (`sha256` of its UTF-8 bytes) and compares it with the caller's `store.HashContent`, so the caller never sends the text back. A transcript's `UpdatedAt` would not do: setting the summary changes it too.

`Recording.Download` holds the download state from migration `00010` (attempts, last failure kind, its time, when to retry). A `Recording` without stored audio has a nil `Audio`; `AudioDeletedAt` is set when its audio was stored and later deleted. `store.TranscriptSegment` has the same fields as `transcribe.Segment` (`Start`, `End`, `Text`, `Speaker`), so the store does not import the transcription client. Times are stored to the millisecond, rounded. A segment with a negative start or an end before its start is `ErrInvalidSegment`, returned before the database is touched. Saving no segments stores an empty array, which reads back as an empty, non-nil slice.

`api/migrations/down_integration_test.go` also rolls back to `00006`, checks both tables are gone and `meeting_events` is not, and applies `00007` again. It also rolls back to `00009`, checks the four download-state columns of `00010` are gone and `users` is not, and applies `00010` again. `agenda_change_event_pending_down_integration_test.go` rolls back to `00010`, checks `change_event_pending` is gone while the download-state columns and `users` are not, and applies `00011` again.

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

`Segment.Speaker` is empty when the model gives no speaker. Otherwise it is `c<chunk>/<model id>`, such as `c0/speaker_0`. Models assign speaker IDs independently for every upload, so `speaker_0` in one chunk is not necessarily the same person as `speaker_0` in the next. The chunk prefix makes the IDs unique across the whole transcript and never claims that two chunks share a speaker, so a stored transcript's speaker labels are per chunk (`c3/speaker_1` is speaker 1 of the fourth ten-minute chunk) and the same person has a new label every 9.5 minutes; identifying the same person across chunks is left to a later step (for example the LLM pass, using the text). A file sent in one request is chunk 0. The labels are stored on every transcript segment but hidden from `GET /v1/meetings/{id}` until speakers can be matched across chunks (see [API](#api)).

### Chunking

The gateway path always re-encodes through `ffprobe` and `ffmpeg` (which must be on `PATH`; the worker image installs Debian's `ffmpeg` package, see [The worker image](#the-worker-image)), unless the file is an `.mp3` at or under `TRANSCRIPTION_DIRECT_UPLOAD_MB`. Chunks are 10 minutes long and overlap by 30 seconds, so each starts 9 minutes 30 seconds after the previous one. Each chunk is re-encoded as 16 kHz mono 32 kbps MP3 (about 2.4 MB, about 3.2 MB as base64). A chunk over `TRANSCRIPTION_MAX_CHUNK_MB` fails with `transcribe.ErrChunkTooLarge`. The `groq` provider uploads a file at or under `TRANSCRIPTION_MAX_UPLOAD_MB` as is and chunks larger ones.

Each chunk's segment (and word) times are shifted by the chunk's offset, and the two views of the 30-second overlap are stitched so that each moment of audio is represented once and no word is cut in half (`internal/transcribe/stitch.go`):

- **Cut in a shared pause.** For each pair of neighbouring chunks the stitcher looks for a cut point inside the overlap, at least half a second from either edge, that lies inside no word of either chunk (when both returned word timings, as `elevenlabs/scribe-v2` does) or inside no segment of either chunk (otherwise), as close to the middle of the overlap as possible. The earlier chunk keeps what lies before the cut and the later chunk what lies after it. A segment cut by a chunk's own edge always lies on the far side of the cut, so a half-heard sentence is dropped in favour of the neighbour's complete one. With word timings, a segment that crosses the cut is trimmed to its words on the kept side.
- **No shared pause.** With word timings, words are assigned to the side holding their midpoint. Without them, the segment crossing the middle is kept from one chunk only: the one that heard it completely (it does not touch that chunk's edge); if both did, the one whose midpoint lies on its side; if neither did, the longer view. Segments of the other chunk that overlap it are dropped. That last case only happens when a segment is longer than the overlap, which the 30-second overlap and the models' segment lengths (Whisper's and our word-built segments are at most 30 s) make rare, and it can lose or repeat the words of that one segment.
- **Monotonic.** The merged segments never overlap: a segment starting before the previous one ends is dropped when its midpoint is inside the previous one, and otherwise starts where the previous one ends.
- **Text without timestamps.** When a chunk returns text but no segments, the whole transcript is text only: the chunks' texts are joined in order, and the longest run of at least 3 words that ends one chunk's text and begins the next (compared without case and punctuation, within 400 words) is kept once. Without such a match both copies stay, so a model without timestamps can repeat up to 30 seconds of speech at a boundary.

`TestChunkStitchingKeepsEveryWordOnceAcrossBoundaries` (in `internal/media`) runs real `ffmpeg` on 25 minutes of generated speech (12-second sentences of 0.3-second words) with a fake model that segments each chunk by silence, so sentences cross both chunk boundaries from both sides: 2,667 words, 0 duplicated, 0 dropped. With the earlier 5-second overlap and midpoint rule the same audio gave 20 duplicated words. The stitched `Text` joins the kept segments, and `Duration` is the `ffprobe` duration of the whole file. `Transcribe` extracts and uploads chunks one at a time; `TranscribeWith` (below) can run up to 8 at once. The temporary directory holding them is removed on every return path, including errors and a canceled context.

`TranscribeWith(ctx, path, transcribe.Options{...})`, from the `transcribe.Resumable` interface the chunking client implements, adds what a long-running job needs. Each field's zero value keeps `Transcribe`'s behaviour.

- `MaxDuration`: the file is probed first (the direct path too), and longer audio fails with `transcribe.ErrTooLong` before any chunk is cut or uploaded.
- `Concurrency`: chunks transcribed at once, 1 to 8. Results are stitched in chunk order, so the transcript is the same as with one at a time. After a failure no new chunk is started; chunks already in flight finish, and their results are checkpointed.
- `Retries` and `RetryDelay`: a budget of extra upload attempts shared by every chunk of the call, spent only on retryable errors. The wait is the 429's `Retry-After`, or `RetryDelay` doubling per attempt, capped at one minute either way: a `Retry-After` longer than a minute is waited for one minute, and the retry still spends one unit of the budget, so a provider asking for long pauses exhausts the budget and the job is retried by River instead. A canceled context ends the wait. A transient error on the last chunk therefore retries that chunk, not the file.
- `Checkpoint`: a `transcribe.Checkpoint` (`Load` and `Save` by `transcribe.Chunk`, its index, start and length). A chunk found by `Load` is neither cut nor uploaded; each chunk transcribed is passed to `Save`. Checkpoints never fail a call: a failed `Load` is a miss.
- `TempDir`: where the chunk directory is created.

### Errors

`Transcribe` does not retry; River retries the job, and without checkpoints a retry restarts the whole file (`TranscribeWith` above retries chunks in place and resumes from checkpoints). `transcribe.IsRetryable(err)` is true for network failures, the `TRANSCRIPTION_TIMEOUT` limit, a canceled context, 408, 429 (with `RetryAfter` from the `Retry-After` header when present), 5xx, 402 on the gateway (the balance can be topped up; `internal/llm` instead treats a 402 as an account failure), and a 200 response that cannot be decoded. These are `*transcribe.Error` values. Everything else is permanent and a worker should wrap it in `river.JobCancel`: other 4xx statuses (including 413), `ErrNotConfigured`, `ErrInvalidProviderOptions`, `ErrChunkTooLarge`, `ErrResponseTooLarge` (a reply over 32 MiB), a missing audio file, and `ffmpeg` or `ffprobe` failures such as unreadable audio or a binary that is not installed.

### Tests

Tests never call the gateway or Groq; the client's own tests run against `httptest` servers and a fake `Splitter`. One test runs real `ffmpeg` on a generated tone and skips when `ffmpeg` or `ffprobe` is not installed. `internal/transcribe/transcribetest` provides `transcribetest.Fake`, a `transcribe.Client` for other packages' tests (`Returning(transcript)`, `Failing(err)`, or a `Respond` function; `Calls()` returns the paths received), and `transcribetest.FakeSplitter`.

## Migrations

Migrations are goose SQL files in `api/migrations/` and follow expand and contract:

- **Expand:** only add tables or columns that old code ignores. Squawk fails CI on destructive or locking changes.
- **Release:** new code that uses them ships behind a flag.
- **Contract:** a later migration removes the old structures. Mark the file with a `-- migration: contract` line, which permits dropping columns and tables, and confirm the flag is fully on in production before merging.

Number a new file above every migration already on `main`, and renumber a branch's migration when another lands first. Goose refuses to apply a migration numbered below the database's current version, so a lower number would fail `migrate up` at deploy. Gaps are fine: `00005` is unused because recordings (`00007`) was renumbered after meeting events (`00006`) merged, and users (`00009`) skipped `00008`, which another branch held; that branch's recording download state became `00010` when users merged first. The user's Stripe customer is `00013` while `00012` and `00014` are held by other branches.

`00013` adds the column and its `UNIQUE` constraint in one ordinary, transactional `ALTER TABLE`, with `-- squawk-ignore disallowed-unique-constraint` on the constraint line. The rule exists because a unique constraint builds its index under an `ACCESS EXCLUSIVE` lock; here the column is new and all null and `users` is small, so the lock is held for milliseconds, and the `ADD COLUMN` takes the same lock anyway. The concurrent alternative (`CREATE UNIQUE INDEX CONCURRENTLY` in a `NO TRANSACTION` file) is worse here: while the API's and the worker's pre-deploy `migrate up` overlap, the second runner waits on the advisory lock holding a snapshot, the concurrent build waits for that snapshot, and neither finishes until the pre-deploy timeout; and a failed concurrent build leaves an invalid index that `IF NOT EXISTS` skips while goose records the version. Use a transactional file unless a table is large enough for the lock to matter.

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
  - `STRIPE_SECRET_KEY` is declared with `preserve()`; `STRIPE_PRICE_ID`, `PUBLIC_WEB_URL` and the WorkOS variables are not set yet, so billing is off in production (see [Billing](#billing)).
- **Worker on Railway**, also in `.railway/railway.ts`: the `civic-scanner-worker` service runs the River worker (`api/cmd/worker`) against the same `Postgres` database. It builds `api/Dockerfile.worker` (`build.dockerfilePath`, relative to the `/api` root directory) from the same commit, with the same watch paths and Wait for CI. It has no start command: the image's entrypoint runs `/app/worker` as PID 1 (see [The worker image](#the-worker-image)).
  - `/app/migrate up` runs as its pre-deploy step too, so the worker never starts new code before its schema exists, whichever service deploys first. Both services may run it at once: `migrations.Up` holds a Postgres advisory lock, so the second run waits and then finds nothing to apply.
  - No health check and no public domain: the worker serves no HTTP. A deploy goes live once the container starts; a crash restarts it up to 5 times.
  - One replica. River elects a leader for periodic jobs, so more replicas would be safe, but one is enough for a daily check.
  - On a redeploy the old worker gets SIGTERM and 30 seconds (`drainingSeconds`) before SIGKILL. The worker stops fetching new jobs and gives running ones 25 seconds to finish. A job cut off stays running in the database until River's rescuer finds it stuck and retries it.
  - Its variables are `DATABASE_URL`, `APP_ENV`, `LOG_LEVEL` and `FEATURE_FLAGS`. `INGEST_CHECK_HOUR_UTC` keeps its default. `FEATURE_FLAGS` is `ingest-check-agencies,ingest-summarize-agendas`, so the daily check runs for every agency with an adapter, `process_agenda` stores each agenda's text and each new version is summarized; turn on another flag by adding it to that list (see [Feature flags](#feature-flags)). `LLM_PROVIDER` is `anthropic`, so summaries call the Anthropic API directly (see [Switching summaries to the Anthropic API](#switching-summaries-to-the-anthropic-api)). `ANTHROPIC_API_KEY`, `AI_GATEWAY_API_KEY` and `FIRECRAWL_API_KEY` are declared with `preserve()`: set their values in Railway, and an apply leaves them as they are. `AI_GATEWAY_API_KEY` is now used only by transcription. Without the Firecrawl key a page that plain HTTP cannot read is not fetched. `ingest-download-audio` is off, so no recording is downloaded. `ingest-summarize-agendas` is on: with the Anthropic key set, each new agenda version is summarized and the sweep summarizes the versions left without one; without the key, summary jobs complete without calling a model. `ingest-agenda-changes` is off as well, so every new agenda text still records `agenda_changed` at once. `ingest-transcribe` is off, so no transcription job is enqueued or calls a model; once it is on and the gateway key is set, the sweep transcribes the stored audio left without a transcript. `ingest-summarize-transcripts` is off, so no transcript is summarized and no `transcript_ready` is recorded; once it is on and the Anthropic key is set, the sweep summarizes the transcripts left without a summary. Its `STORAGE_*` variables reference the `audio` bucket the same way the API service's do.
- **Web on Vercel**, with the project root directory set to `web`. Each pull request gets a preview deploy.

### Changing Railway configuration

`.railway/railway.ts` owns the Railway project, including every variable on its services. Change settings there, not in the dashboard: the next apply reverts dashboard edits. Secrets go in as `preserve()` so their values stay out of the repository.

The `Railway config` workflow (`.github/workflows/railway-config.yml`) plans and applies it, using the [`railwayapp/config`](https://github.com/railwayapp/config) action:

- A pull request that changes `.railway/` gets a comment with the plan: what Railway would change, with destructive changes marked. The plan, the environment's state and the `.railway/` tree are pinned in an artifact.
- After the merge the apply job waits for approval: it runs in the `railway-production` GitHub environment, which has a required reviewer, so GitHub asks that reviewer to approve or reject the run. Nothing changes in Railway until it is approved, and a rejected or expired request applies nothing. This covers every change, destructive ones included, so read the plan comment before approving.
- Once approved, the job applies the pinned plan and does not evaluate the file again. It fails, changing nothing, when the environment changed after the plan (a dashboard edit or another apply), when the merged `.railway/` tree is not the planned one, or when there is no plan for the merged commit. Open a new pull request to plan again, or apply by hand.
- A push straight to `main` has no plan and applies nothing. Pull requests from forks are skipped, since they get no secrets.

The workflow needs two things set up in GitHub:

- A repository secret, `RAILWAY_TOKEN`: a Railway [project token](https://docs.railway.com/integrations/api#project-token) for the production environment. The token can change that environment only.
- An environment named `railway-production` (Settings, Environments) with a required reviewer. Without the required reviewer GitHub creates the environment on first use with no protection, and the apply job runs as soon as the pull request merges.

The root `package.json` and `pnpm-lock.yaml` exist for this workflow: they pin the `railway` package that `.railway/railway.ts` imports.

By hand, with the [Railway CLI](https://docs.railway.com/cli) installed:

```sh
railway link            # once: pick the project and the production environment
railway config plan     # preview the change; changes nothing
railway config apply    # apply it after reviewing the plan
```

`railway config pull` re-imports the project from Railway. Generated Railway domains are not in the file and are left alone.

## Backups and restore

The production database is backed up by Railway's [volume backups](https://docs.railway.com/volumes/backups): incremental copy-on-write snapshots of `postgres-volume`, the volume mounted at `/var/lib/postgresql/data` on the `Postgres` service.

### The schedule

The schedule was set on 2026-11-19 with the Railway CLI:

```sh
railway postgres pitr schedule set --daily --weekly --service Postgres
```

| Schedule | When (UTC) | Kept for |
| --- | --- | --- |
| Daily | 03:27 every day (cron `27 3 * * *`) | 6 days |
| Weekly | 20:47 on Saturdays (cron `47 20 * * 6`) | 27 days |

Railway fixes the retention of each schedule; the CLI has no retention option. A single backup can be kept forever with `railway postgres pitr backup lock <id>`.

The schedule is **not** declared in `.railway/railway.ts`. The [IaC reference](https://docs.railway.com/infrastructure-as-code/reference) documents no backup field for databases or volumes, `railway config pull` does not import the schedule, and `railway config plan` ignores it, so an apply leaves it as it is. It lives only in Railway, so manage it with the CLI:

- **See it:** `railway postgres pitr schedule list --service Postgres` (add `--json` for the cron times and retention in seconds).
- **Change it:** run `schedule set` again with the schedules wanted (`--daily`, `--weekly`, `--monthly`, in any combination). Monthly backups are kept 89 days.
- **Remove it:** `railway postgres pitr schedule set --none --service Postgres`. Existing backups are kept until they expire.
- **Re-check it after anything that replaces the volume**, a restore included: a restore mounts a new volume, and whether the schedule carries over to it is not verified (see step 7 of the restore).

### What is backed up

- **What:** the whole Postgres data directory, so every table: agencies, meetings, agenda text and summaries, transcripts, users, the goose and River migration tables and the River job queue.
- **Where to see backups:** the `Postgres` service's **Backups** tab in the dashboard, or `railway postgres pitr backup list --service Postgres`.
- **On demand:** `railway postgres pitr backup create --service Postgres --name pre-migration` takes one now, for example before a risky migration. A manual backup is limited to half the volume's size (25 GB of the 50 GB volume).
- **Size and cost:** the volume holds about 1 GB. Backups are billed at the volume storage rate on the data exclusive to each backup, since they are incremental, so the cost follows how much changes between backups rather than the full 1 GB each time.

What these backups do **not** cover:

- **Losing the project, the account or the volume.** Backups live in the same Railway project as the volume. Wiping the volume deletes all its backups, and a backup can only be restored into the same project and environment. There is no copy outside Railway.
- **Anything older than the retention window.** Damage noticed after 6 days can only go back to a weekly backup, and after 27 days to none.
- **The `audio` bucket.** Nothing copies bucket objects. That is acceptable for now: `ingest-download-audio` is off in production, so the bucket holds little or nothing, recordings are public and can be downloaded again from the agencies' sites while they still host them, and objects are meant to be deleted after about six months anyway (see [Object storage](#object-storage)). Transcripts made from the audio live in Postgres and are backed up. Revisit this once audio downloads are on and a recording might no longer be available at its source.

Railway also offers [point-in-time recovery](https://docs.railway.com/volumes/point-in-time-recovery) for Postgres (continuous WAL archiving to a bucket, a window of about 4 weeks). It is off.

### What was verified

Verified against the real `production` environment on 2026-11-19:

- Before the schedule was set there was no schedule, no backup, and PITR was disabled.
- After `schedule set`, `schedule list --json` shows the two schedules, times and retention in the table above.
- With `.railway/railway.ts` as on `main`, `railway config plan` reports the configuration up to date, `railway config pull --json` has no backup field, and setting the schedule did not restart `Postgres`.

Not verified, taken from Railway's documentation or inferred: that backups appear at the scheduled times, the whole restore flow below, how the API and the worker behave while `Postgres` restarts, that a redeploy re-runs the migrations a restore rolled back, whether the schedule carries over to the restored volume, and how `railway config plan` treats renamed volumes. Rehearse a restore when there is a safe moment, ideally in a non-production environment, before relying on these steps in an incident.

### Restoring a backup

A restore replaces the database with the backup's contents. Everything written after the backup was taken is lost: new agenda versions, summaries, transcripts, users, finished and queued jobs. Most of it is recreated by the next daily check and the sweeps; users who signed up in between have to sign up again.

1. **Freeze Railway configuration.** `.railway/railway.ts` is authoritative for the whole project: a resource the file does not declare is planned for deletion ("omitting a resource means deleting it", in [Railway's words](https://docs.railway.com/infrastructure-as-code)). After a restore the live data sits on a new volume with a date-stamped name that the file does not declare, while the file still declares `postgres-volume`, now the old one. Any apply before step 8 would plan to destroy the volume holding the live data and mount the old one again. The `Railway config` workflow applies `.railway/` changes once a reviewer approves the run, so until step 8 is done: merge no pull request that touches `.railway/`, reject any `Railway config` apply job waiting for approval, and do not run `railway config apply` by hand.
2. **Pick the backup.** Note when the damage happened (a deploy, a migration, a job run). In the `Postgres` service's **Backups** tab, or with `railway postgres pitr backup list --service Postgres`, choose the newest backup taken before that time.
3. **Restore.** In the **Backups** tab, click **Restore** on that backup and wait, from seconds to minutes. Railway stages the change: open **Details** at the top of the project canvas and check that a new volume, named after the backup's date stamp, is mounted at `/var/lib/postgresql/data` and that `postgres-volume` is unmounted. Click **Deploy**. `Postgres` redeploys on the restored volume. The old volume stays in the project, unmounted, with its data and the backups newer than the restored one, so the restore can be reversed by mounting it again.
   - The CLI equivalent is `railway postgres pitr backup restore <id> --service Postgres`, which Railway describes as an in-place restore for a standalone database. Prefer the dashboard, which shows the staged change before anything happens.
4. **The API and the worker during the restore.** Leave them running. While `Postgres` redeploys, requests that need the database fail, and running jobs fail and are retried by River. Both should reconnect once `Postgres` is back, without a redeploy. If either keeps logging connection errors after a few minutes, run `railway restart --service civic-scanner` or `railway restart --service civic-scanner-worker`. The job queue goes back to the backup's state: jobs that finished after the backup may run again, and jobs enqueued after it are gone until the next daily check or sweep enqueues them.
5. **Verify the data.** Open a shell with `railway connect Postgres` (needs `psql` locally) and check:
   - `SELECT max(version_id) FROM goose_db_version;` is a migration number from `api/migrations/` at or before the newest one.
   - `SELECT count(*) FROM agencies;`, `meetings`, `agenda_versions` and `users` return plausible counts, not zero.
   - `SELECT max(created_at) FROM agenda_versions;` is shortly before the backup's time and not after the damage.
   - The API's `/v1/healthz` returns 200.
6. **Roll forward migrations.** If the backup predates migrations that the running code needs, the schema is behind the code. Run `railway redeploy --service civic-scanner`: its pre-deploy step, `/app/migrate up`, should apply the missing goose and River migrations before the API restarts. Redeploy `civic-scanner-worker` the same way, then check `goose_db_version` again. If a migration itself caused the damage, revert it on `main` first so it does not run again.
7. **Check the backup schedule.** Run `railway postgres pitr schedule list --service Postgres`. If the daily and weekly schedules are missing on the restored volume, set them again with the command in [The schedule](#the-schedule).
8. **Reconcile `.railway/railway.ts`.**
   1. Rename the volumes so the restored one carries the declared name: `railway volume update --volume postgres-volume --name postgres-volume-replaced-<date>`, then `railway volume update --volume <date-stamp name> --name postgres-volume`.
   2. The old volume, `postgres-volume-replaced-<date>`, is now undeclared, so a plan would destroy it. Declare it temporarily in `.railway/railway.ts` with a `volume("postgres-volume-replaced-<date>", { ... })` whose settings match the old volume (region `sfo`, its size and alerts), and add it to `resources`.
   3. Run `railway config plan` and act on what it shows:
      - Nothing to change, or only `postgres-volume`'s settings (size, alerts) being set back to what the file declares: open a pull request with the temporary declaration, check that its plan comment shows the same, and approve the apply after merging. The freeze ends here.
      - A destroy of `postgres-volume` or of the replaced volume: a name in the file does not match a volume in Railway. Compare with `railway volume list`, fix the name, and plan again.
      - A change to which volume `Postgres` mounts, or anything else touching `Postgres`: do not apply. Check in the dashboard that `Postgres` mounts the restored volume, and get a second person to review before going further.
9. **Clean up later.** Once the restored data has been confirmed for a few days, remove the temporary `volume(...)` declaration in a pull request. Its plan now shows a destroy of `postgres-volume-replaced-<date>`; that is expected, and approving the apply deletes the old volume together with the backups that only it holds.
