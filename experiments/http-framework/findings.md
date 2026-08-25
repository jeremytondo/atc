# HTTP framework spike: stdlib `net/http` vs Huma v2

ATC-259's first task. Both candidates implement the identical chassis
surface — bearer-authed `GET /v1/health` (token required on loopback too),
version headers both ways, 401-before-routing on unknown paths — and pass
the same conformance suite (`internal/conformance`). Judged on the four
ATC-259 criteria. Huma at v2.39.1, Go 1.26.7, 2026-08-24.

## What was established

### 1. OpenAPI generation path (the ATC-247 §1 criterion)

Proven end-to-end, not just claimed: the Huma candidate's Go output struct
produced an OpenAPI 3.1 document (served at `/openapi.json`, snapshot in
`evidence/openapi.json`), and `npx openapi-typescript@7.13.0` compiled it
without warnings into clean TypeScript (`evidence/atc-api-types.ts`) —
enum values, required-vs-optional, and `doc:` tags all carried through,
plus the bearer security scheme declared. The Go struct is the single
source; nothing was written twice.

The stdlib candidate has no generation path. The options if it were
chosen: hand-write the OpenAPI document (a second copy of every contract,
drifting), bolt on a comment-parsing generator like swaggo (OpenAPI 2.0,
annotation drift), or adopt a framework later anyway.

### 2. Middleware ergonomics — a tie, by construction

`humago` mounts Huma on a standard `http.ServeMux`, so auth and
version-header middleware are plain `net/http` middleware and the code is
*byte-for-byte identical* in both candidates. Choosing Huma does not
change how middleware is written, and existing `net/http` knowledge
transfers whole.

One sharp edge found: Huma's own operation middleware
(`api.UseMiddleware`) only runs for registered operations, so it cannot
enforce auth on unknown paths or on `/openapi.json` itself. Conclusion
recorded so the chassis doesn't relearn it: **auth and version headers
belong in transport-level middleware wrapping the mux, never in Huma
middleware.** With that rule, the framework is invisible to the security
posture.

### 3. Dependency weight — smaller than expected

- One direct dependency; `go.sum` is 18 lines.
- The linked server binary contains **zero third-party packages beyond
  Huma's own** (verified with `go list -deps`): cbor/uuid/chi in `go.sum`
  serve opt-in subpackages and tests only.
- Stripped static binary: 6.0 MB stdlib vs 7.1 MB Huma (+1.1 MB).
- Handler-package LOC for this surface: 63 stdlib vs 91 Huma — Huma pays
  a one-time config tax here; the per-route cost inverts as soon as
  routes have request bodies and validation.

### 4. Cost of deferring adoption

The Huma handler signature is `func(ctx, *Input) (*Output, error)` with
struct-tag validation; the stdlib equivalent hand-rolls decode, validate,
error-shape, and encode per route. Deferring Huma past this ticket means
Terminals (ATC-251) hand-rolls that for its whole resource surface and
someone later rewrites every handler signature to adopt the framework —
the migration cost grows with exactly the routes ATC-251 is about to add.
Adopting at the chassis, when there is one route, makes the migration
cost ~zero. The reverse move (Huma → stdlib) is also cheap at one route,
so the choice is not a lock-in either way *today*; it becomes one at the
first real resource.

## Incidental observations

- Huma's `DefaultConfig` injects a `$schema` field into response bodies
  and a `Link: describedBy` header. Harmless under the additive-only rule
  and disableable (omit the schema-link transformer) if unwanted.
- Errors default to RFC 7807 `application/problem+json` with a documented
  `ErrorModel` schema — a ready-made error contract the stdlib candidate
  would have to invent.
- Huma ships an SSE subpackage (`huma/v2/sse`) that types events into the
  OpenAPI document — directly relevant to the ATC-251 `/v1/events` feed,
  though not exercised in this spike.
- `/docs` (interactive API reference) comes for free behind the same
  token.

## Assessment against the criteria

Three criteria favor Huma (OpenAPI is generated not hand-kept, later
adoption is the expensive path, incidental error/SSE/docs wins) and the
fourth is neutral (middleware identical by construction), at a cost of
one dependency and ~1.1 MB of binary. The evidence points at adopting
Huma in the chassis; the decision itself is recorded in ATC-259.

## Running it

`go test ./...` runs both candidates against the conformance suite.
`go run ./cmd/huma-server` (port 7332, token `atc_spike-token`) serves
`/v1/health`, `/openapi.json`, and `/docs`; `go run ./cmd/stdlib-server`
is the same surface on 7331 minus the OpenAPI routes.
