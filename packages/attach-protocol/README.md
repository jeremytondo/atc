# Terminal Attach Protocol

The wire protocol for the App Server's WebSocket terminal attach — everything
a client implementer needs, stated once. The server-side authority is
`app-server/src/terminals/attachProtocol.ts` (imported by the server bridge
and the CLI client); this document and the machine-readable vectors in
`fixtures/` exist so non-TypeScript clients (ATCKit/macOS) can implement and
test against the same rules without reading server source. The endpoint is
deliberately excluded from `openapi.json` — an OpenAPI document cannot
represent a WebSocket upgrade.

Consume `fixtures/` from tests (see "Test vectors" below) so drift between
clients is caught mechanically, not in code review.

## URL

```
ws://<host>/api/v1/terminals/{terminalId}/attach?cols=<n>&rows=<n>
```

- Scheme: `ws:` for `http:` base URLs, `wss:` for `https:`.
- `cols`/`rows` set the initial PTY size. Both are optional; a missing or
  malformed value falls back to the server default (80×24). Values are
  floored and clamped into **[1, 1000]** — the same rule applied to `resize`
  frames.

## Pre-upgrade HTTP statuses

The server validates before upgrading; a failed handshake is a plain HTTP
response, never a WebSocket close:

| Status | Meaning | Client action |
| ------ | ------- | ------------- |
| `404` | No terminal with this id. | Refetch terminals; drop the attach. |
| `410` | The terminal ended (tombstone). | Show ended state; offer delete. |
| `426` | Missing/invalid `Upgrade: websocket` header. | Client bug; do not retry. |

## Framing

- **Binary frames are terminal bytes, both directions.** Client → server:
  keystrokes/paste input. Server → client: PTY output. No message framing
  inside — write bytes straight to the terminal emulator.
- **Text frames are JSON control messages.** Unknown or malformed control
  frames are ignored by both sides — never fatal, never echoed.

### Control vocabulary

| Frame | Direction | Semantics |
| ----- | --------- | --------- |
| `{"type":"resize","cols":120,"rows":40}` | client → server | Resize the PTY. Values floored and clamped into [1, 1000]. |
| `{"type":"ping"}` | server → client | App-level keepalive, sent every **30 s**. |
| `{"type":"pong"}` | client → server | Answer to `ping`. A client silent for **120 s** is closed with `1011 ping_timeout`. |

Answer every `ping` with a `pong` promptly; WebSocket protocol-level pings are
not used (Bun's server API does not surface them).

## Close vocabulary

| Code | Reason | Retryable | Meaning |
| ---- | ------ | --------- | ------- |
| 1000 | `terminal_ended` | no | **Authoritative.** A complete multiplexer inventory proved the session is gone; the tombstone is already written. Refetch the terminal and show ended state. |
| 1000 | `detach` | — | Deliberate client detach (sent by the client). The session keeps running; reattach at will. |
| 1011 | `attach_failed` | yes | The bridge could not attach or lost its PTY client. Says nothing about the terminal's existence. |
| 1011 | `zmx_unavailable` | yes | The multiplexer could not be consulted. Says nothing about the terminal's existence. |
| 1011 | `ping_timeout` | yes | The server gave up on an unresponsive client. Reconnect when the client is healthy again. |
| 1006 | — | yes | Abnormal closure (transport loss, server restart, connection refused). Not server-sent — but treat it exactly like a retryable 1011: reconnect with backoff, and refetch the terminal first to learn whether it still exists. |

"Retryable" means: the terminal may well still be live — refetch it
(`GET /api/v1/terminals/{terminalId}`) and reattach if it is. Only
`terminal_ended` proves death.

## Sizing and the zmx leader

The multiplexer (zmx) sizes each session to its **leader** client — in
practice, the most recent attach. Consequences a client must design for:

- A non-leader's `resize` frames are **silently ignored**; no frame ever
  reports the effective size. Do not assume the PTY matches what you sent.
- On (re)attach, zmx repaints the full screen plus scrollback to the new
  client. This repaint is the recovery mechanism: whatever output or sizing
  you missed while detached or non-leader is healed by reattaching.
- Multiple simultaneous attaches to one terminal work (bytes fan out), but
  only the leader controls size.

## Reconnect flow

1. Attach closes (1011 reason, or 1006).
2. `GET /api/v1/terminals/{terminalId}` — reconciled read; a dead session
   becomes an `ended` tombstone here.
3. If `live`, reattach with current `cols`/`rows` (this also makes you the
   leader and triggers the full repaint). If `ended`, stop.

Pair the attach with the SSE event stream: a `terminal` `updated` event is
the push signal that a terminal you care about changed state.

## Test vectors

`fixtures/` holds shared JSON vectors; the TypeScript suite
(`app-server/test/terminals/attachProtocolFixtures.test.ts`) consumes them
today, and ATCKit's attach implementation must consume the same files.

- `control-frames.json` — wire ↔ frame pairs every implementation must
  round-trip, plus malformed frames every implementation must ignore.
- `close-codes.json` — the close vocabulary above, with retryability.
- `dimension-clamp.json` — the [1, 1000] floor/clamp/fallback rule as
  input → expected cases (shared by query params and resize frames).
- `attach-urls.json` — base URL + terminal id + size → expected attach URL.
