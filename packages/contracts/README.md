# API Contract Fixtures

**Scope: the legacy Go server (`server/`) only.** The active App Server
(`app-server/`) derives its contract from `app-server/src/api.ts` and the
generated `app-server/openapi.json` instead — these fixtures do not govern it.

`fixtures/` pins the legacy API's wire shapes. Each file holds one
request/response shape plus the `routes` that use it.

The one thing worth knowing before you touch them: **three suites in three
languages read these files**, so a change here fails in places you weren't
working on.

- `server/internal/api/contract_test.go` — round-trips each fixture through the
  Go wire structs, and fails if a registered route appears in no fixture's
  `routes` (the WebSocket attach is exempt).
- `packages/ATCKit/Tests/ATCAPITests/ContractFixtureTests.swift` — decodes each
  response into the Kit models the macOS app uses.
- `server/web/src/lib/api.contract.test.ts` — type assertions and
  mocked-fetch decoding for the web client.

A wire change therefore needs `mise run check` at the repo root, not just
`mise run -C server test`.
