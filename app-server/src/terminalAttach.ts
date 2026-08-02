import { Cause, Effect, Queue, Stream } from "effect"
import type { Scope } from "effect"
import type { HttpServerRequest } from "effect/unstable/http"
import { HttpServerResponse } from "effect/unstable/http"
import * as Socket from "effect/unstable/socket/Socket"
import { DEFAULT_PTY_COLS, DEFAULT_PTY_ROWS } from "./subprocess.ts"
import type { Terminals } from "./terminals.ts"

// The WebSocket attach bridge (ATC-130): one upgraded socket bridged onto
// one scoped `zmx attach` PTY client. Wire protocol and close vocabulary
// are stated once, on the contract endpoint (api.ts `attachTerminal`);
// this module implements them.
//
// Flow control: Bun's WS writer exposes no send-backpressure signal, so a
// slow client cannot throttle the PTY. Memory stays bounded anyway — the
// PTY output queue is bounded and sliding (oldest chunks drop; zmx repaints
// the full screen on reattach, so lost history is self-healing) — and a
// client that stops answering the app-level pings is closed, its PTY client
// reaped by the closing scope.

const PING_INTERVAL_MILLIS = 30_000
/** A client is dead once it has ignored pings for this long. */
const PONG_TIMEOUT_MILLIS = 120_000
/** Outbound frames buffered ahead of the socket writer. */
const OUTBOX_CAPACITY = 256

const clampDimension = (raw: string | number | undefined, fallback: number): number => {
  const parsed = typeof raw === "number" ? raw : Number.parseInt(raw ?? "", 10)
  return Number.isNaN(parsed) ? fallback : Math.max(1, Math.min(1_000, Math.floor(parsed)))
}

const decodeControl = (
  text: string,
): { type: "resize"; cols: number; rows: number } | { type: "pong" } | { type: "ignored" } => {
  try {
    const parsed = JSON.parse(text) as { type?: string; cols?: number; rows?: number }
    if (
      parsed.type === "resize" &&
      typeof parsed.cols === "number" &&
      typeof parsed.rows === "number"
    ) {
      return {
        type: "resize",
        cols: clampDimension(parsed.cols, DEFAULT_PTY_COLS),
        rows: clampDimension(parsed.rows, DEFAULT_PTY_ROWS),
      }
    }
    if (parsed.type === "pong") return { type: "pong" }
  } catch {
    // Unknown or malformed control frames are ignored, never fatal.
  }
  return { type: "ignored" }
}

/**
 * The raw handler implementing the contract's `attachTerminal` endpoint.
 * Services (and the long-lived bridge scope) are resolved at handler-group
 * build so the request effect has no requirements of its own.
 *
 * The bridge is forked into the server's scope rather than run inside the
 * request effect: Bun aborts the original HTTP request once the protocol
 * switches, and the adapter interrupts the request fiber on abort — a
 * bridge living there would be torn down the moment it started.
 */
export const attachTerminal =
  (services: { readonly terminals: Terminals["Service"]; readonly bridgeScope: Scope.Scope }) =>
  (ctx: {
    readonly params: { readonly terminalId: string }
    readonly query: { readonly cols?: string; readonly rows?: string }
    readonly request: HttpServerRequest.HttpServerRequest
  }) =>
    Effect.gen(function* () {
      const { terminals } = services

      // Pre-upgrade rejection with a real HTTP status: a reconciled read, so
      // a terminal whose session died since the last look is already a
      // tombstone here. No zmx child is spawned for a failed handshake.
      const terminal = yield* terminals
        .get(ctx.params.terminalId)
        .pipe(Effect.catchTag("TerminalNotFound", () => Effect.succeed(undefined)))
      if (terminal === undefined) return HttpServerResponse.empty({ status: 404 })
      if (terminal.status === "ended") return HttpServerResponse.empty({ status: 410 })
      if (ctx.request.headers["upgrade"]?.toLowerCase() !== "websocket") {
        return HttpServerResponse.empty({ status: 426 })
      }

      const size = {
        cols: clampDimension(ctx.query.cols, DEFAULT_PTY_COLS),
        rows: clampDimension(ctx.query.rows, DEFAULT_PTY_ROWS),
      }

      // Uninterruptible across upgrade-and-fork: the abort that follows the
      // protocol switch must find the bridge already out of this fiber.
      const upgraded = yield* Effect.uninterruptible(
        Effect.gen(function* () {
          const socket = yield* ctx.request.upgrade.pipe(
            Effect.catch(() => Effect.succeed(undefined)),
          )
          if (socket === undefined) return false
          yield* runBridge(terminals, ctx.params.terminalId, size, socket).pipe(
            Effect.scoped,
            Effect.catchCause((cause) =>
              Cause.hasInterruptsOnly(cause)
                ? Effect.void
                : Effect.logWarning("attach bridge ended abnormally").pipe(
                    Effect.annotateLogs({ cause: String(cause) }),
                  ),
            ),
            Effect.forkIn(services.bridgeScope),
          )
          return true
        }),
      )
      return HttpServerResponse.empty({ status: upgraded ? 200 : 426 })
    })

/** The bridge proper. Exported for the deterministic in-process tests; the
 * production entry is `attachTerminal`, which forks this into the server's
 * bridge scope after the upgrade. */
export const runBridge = (
  terminals: Terminals["Service"],
  terminalId: string,
  size: { readonly cols: number; readonly rows: number },
  socket: Socket.Socket,
) =>
  Effect.gen(function* () {
    const write = yield* socket.writer

    /** Close before the main loop has started (attach-time failures). */
    const closeNow = (event: Socket.CloseEvent) =>
      socket
        .runRaw(() => undefined, {
          onOpen: write(event).pipe(Effect.catch(() => Effect.void)),
        })
        .pipe(
          Effect.timeout("1 second"),
          Effect.catch(() => Effect.void),
        )

    /** 1000 terminal_ended iff absence is confirmed; retryable otherwise. */
    const closeConfirming = (fallbackReason: string) =>
      terminals.confirmEnded(terminalId).pipe(
        Effect.map((ended) =>
          ended
            ? new Socket.CloseEvent(1000, "terminal_ended")
            : new Socket.CloseEvent(1011, fallbackReason),
        ),
        Effect.catchTag("ZmxUnavailable", () =>
          Effect.succeed(new Socket.CloseEvent(1011, "zmx_unavailable")),
        ),
      )

    const bail = (event: Effect.Effect<Socket.CloseEvent>) =>
      event.pipe(Effect.flatMap(closeNow), Effect.as(undefined))

    const connection = yield* terminals.attach(terminalId, size).pipe(
      Effect.catchTags({
        // The pre-flight said live but the session is gone: confirm and
        // close with the authoritative code when possible.
        SessionNotFound: () => bail(closeConfirming("attach_failed")),
        ZmxUnavailable: () => bail(Effect.succeed(new Socket.CloseEvent(1011, "zmx_unavailable"))),
        SessionOperationFailed: () =>
          bail(Effect.succeed(new Socket.CloseEvent(1011, "attach_failed"))),
      }),
    )
    if (connection === undefined) return

    // One writer drains one ordered outbox so terminal bytes, keepalives,
    // and the close frame cannot interleave.
    const outbox = yield* Queue.make<Uint8Array | string | Socket.CloseEvent>({
      capacity: OUTBOX_CAPACITY,
    })
    let lastPong = Date.now()

    // PTY -> outbox. The stream ending means this attach client died —
    // never proof the session ended; confirm before saying so.
    yield* connection.output.pipe(
      Stream.runForEach((chunk) => Queue.offer(outbox, chunk)),
      Effect.catchTag("SubprocessError", () => Effect.void),
      Effect.andThen(closeConfirming("attach_failed")),
      Effect.flatMap((event) => Queue.offer(outbox, event)),
      Effect.forkScoped,
    )

    // Keepalive: dead clients must not hold a PTY client forever.
    yield* Effect.gen(function* () {
      for (;;) {
        yield* Effect.sleep(PING_INTERVAL_MILLIS)
        if (Date.now() - lastPong > PONG_TIMEOUT_MILLIS) {
          yield* Queue.offer(outbox, new Socket.CloseEvent(1011, "ping_timeout"))
          return
        }
        yield* Queue.offer(outbox, JSON.stringify({ type: "ping" }))
      }
    }).pipe(Effect.forkScoped)

    const drainOutbox = Effect.gen(function* () {
      for (;;) {
        const item = yield* Queue.take(outbox)
        yield* write(item)
        if (item instanceof Socket.CloseEvent) return
      }
    })

    // Socket -> PTY: binary frames are keystrokes, text frames control.
    // A failed write means input is being discarded (dead PTY client, short
    // write): end the bridge retryably rather than swallowing keystrokes —
    // without inferring the terminal ended, which only an inventory proves.
    const readSocket = socket.runRaw((message) => {
      if (typeof message === "string") {
        const control = decodeControl(message)
        if (control.type === "resize") {
          return Effect.ignore(connection.resize({ cols: control.cols, rows: control.rows }))
        }
        if (control.type === "pong") lastPong = Date.now()
        return undefined
      }
      return connection
        .write(message)
        .pipe(
          Effect.catch(() =>
            Effect.asVoid(Queue.offer(outbox, new Socket.CloseEvent(1011, "attach_failed"))),
          ),
        )
    })

    // Whichever side finishes first ends the bridge; the closing scope
    // detaches the PTY client (never the session). Socket closes — even
    // clean ones — surface as errors here and are normal endings.
    yield* Effect.raceFirst(drainOutbox, readSocket).pipe(Effect.catch(() => Effect.void))
  })
