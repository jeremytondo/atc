# API Contract Fixtures

`fixtures/` is the single source of truth for the atc HTTP API's wire
shapes. Each file pins one request/response shape and lists every route that
uses it:

```json
{
  "routes": ["GET /sessions/{id}", "POST /sessions/{id}/terminate"],
  "request": { "...optional request body...": "" },
  "response": { "...canonical response body...": "" }
}
```

Three test suites consume the same files, so a wire change fails everywhere
that hasn't caught up:

- **Go** (`server/internal/api/contract_test.go`) — round-trips each fixture
  through the producing wire structs and requires every registered route
  (except the WebSocket attach) to appear in exactly this fixture set.
- **Swift** (`packages/ATCKit/.../ContractFixtureTests.swift`) —
  decodes each response into the Kit models the macOS app uses.
- **TypeScript** (`server/web/src/lib/api.contract.test.ts`) — `satisfies`
  assertions against the web client's types plus mocked-fetch decoding tests.

## Changing the API

1. Change the Go wire struct/handler and update the fixture in the same
   change; `mise run -C server test` tells you if they disagree.
2. Run the Swift and web suites (`mise run check` at the repo root) and update
   the client models the failures point at.
3. Adding a route? The Go coverage test fails until the route appears in a
   fixture's `routes`.

Keep fixture values representative and populate optional fields somewhere in
the set — a field no fixture exercises is a field no client is tested against.
The error fixtures pin the common envelope plus session-specific
`session_ended` and `zmx_unavailable` responses.
