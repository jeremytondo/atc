import { Console, Context, Effect, Layer, Queue } from "effect"
import {
  CLOSE_DETACH,
  CLOSE_TERMINAL_ENDED,
  attachUrl,
  decodeControlFrame,
  encodeControlFrame,
} from "../../app-server/src/terminals/attachProtocol.ts"
import * as AppServer from "./appServer.ts"

// Scoped raw-TTY WebSocket bridge for one terminal. Retryable closes are
// reconciled through GET /terminals/{id} before reconnecting. Ctrl-\\ matches
// zmx's native detach binding, but is handled locally so the manager returns
// immediately instead of treating the server-side zmx client exit as a lost
// connection and reconnecting it.

interface BunClientWebSocket extends WebSocket {
  terminate(): void
}

const BunWebSocket = WebSocket as unknown as new (
  url: string | URL,
  options?: Bun.WebSocketOptions,
) => BunClientWebSocket
export const DETACH_BYTE = 0x1c // Ctrl-\\, matching zmx

export type AttachOutcome = { readonly type: "detached" } | { readonly type: "terminalEnded" }

type SocketEnd =
  | { readonly type: "detached"; readonly livedMillis: number }
  | { readonly type: "terminalEnded"; readonly livedMillis: number }
  | { readonly type: "retryable"; readonly reason: string; readonly livedMillis: number }

class AttachInput {
  private socket: BunClientWebSocket | undefined
  private detached = false
  private readonly stdin = process.stdin
  private readonly wasRaw = this.stdin.isTTY === true && this.stdin.isRaw

  constructor(private readonly detachedQueue: Queue.Queue<void>) {}

  start(): this {
    if (this.stdin.isTTY === true) this.stdin.setRawMode(true)
    this.stdin.resume()
    this.stdin.on("data", this.onData)
    process.on("SIGWINCH", this.onResize)
    return this
  }

  stop(): void {
    this.stdin.off("data", this.onData)
    process.off("SIGWINCH", this.onResize)
    if (this.stdin.isTTY === true) this.stdin.setRawMode(this.wasRaw)
    this.stdin.pause()
    this.closeSocket()
    process.stdout.write("\x1b[?25h\x1b[0m\r\n")
  }

  setSocket(socket: BunClientWebSocket): void {
    this.socket = socket
    if (this.detached) socket.close(1000, CLOSE_DETACH)
  }

  clearSocket(socket: BunClientWebSocket): void {
    if (this.socket === socket) this.socket = undefined
  }

  isDetached(): boolean {
    return this.detached
  }

  waitForDetach(): Effect.Effect<void> {
    return Queue.take(this.detachedQueue)
  }

  sendResize(): void {
    const socket = this.socket
    if (socket?.readyState === WebSocket.OPEN && process.stdout.isTTY === true) {
      socket.send(
        encodeControlFrame({
          type: "resize",
          cols: process.stdout.columns,
          rows: process.stdout.rows,
        }),
      )
    }
  }

  private readonly onResize = () => {
    this.sendResize()
  }

  private readonly onData = (chunk: Buffer) => {
    const detachAt = chunk.indexOf(DETACH_BYTE)
    const payload = detachAt === -1 ? chunk : chunk.subarray(0, detachAt)
    if (payload.length > 0 && this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(new Uint8Array(payload))
    }
    if (detachAt === -1 || this.detached) return

    this.detached = true
    Queue.offerUnsafe(this.detachedQueue, void 0)
    this.closeSocket()
  }

  private closeSocket(): void {
    const socket = this.socket
    this.socket = undefined
    if (socket?.readyState === WebSocket.OPEN || socket?.readyState === WebSocket.CONNECTING) {
      socket.close(1000, CLOSE_DETACH)
    }
  }
}

export const classifyClose = (
  code: number,
  reason: string,
  detached: boolean,
  livedMillis: number,
): SocketEnd => {
  if (detached || (code === 1000 && reason === CLOSE_DETACH)) {
    return { type: "detached", livedMillis }
  }
  if (code === 1000 && reason === CLOSE_TERMINAL_ENDED) {
    return { type: "terminalEnded", livedMillis }
  }
  return {
    type: "retryable",
    reason: `${code} ${reason || "no reason"}`,
    livedMillis,
  }
}

const socketHeaders = (config: AppServer.AppServer["Service"]["config"]): Record<string, string> =>
  config.token === undefined ? {} : { authorization: `Bearer ${config.token}` }

const runSocket = (
  url: URL,
  config: AppServer.AppServer["Service"]["config"],
  input: AttachInput,
): Effect.Effect<SocketEnd> =>
  Effect.callback<SocketEnd>((resume) => {
    let socket: BunClientWebSocket
    try {
      socket = new BunWebSocket(url, { headers: socketHeaders(config) })
    } catch (error) {
      resume(
        Effect.succeed({
          type: "retryable",
          reason: error instanceof Error ? error.message : String(error),
          livedMillis: 0,
        }),
      )
      return Effect.void
    }

    socket.binaryType = "arraybuffer"
    input.setSocket(socket)
    let done = false
    let openedAt: number | undefined

    const finish = (result: SocketEnd) => {
      if (done) return
      done = true
      input.clearSocket(socket)
      socket.onopen = null
      socket.onmessage = null
      socket.onerror = null
      socket.onclose = null
      resume(Effect.succeed(result))
    }

    socket.onopen = () => {
      openedAt = Date.now()
      input.sendResize()
    }
    socket.onmessage = (event) => {
      if (typeof event.data === "string") {
        if (decodeControlFrame(event.data)?.type === "ping") {
          socket.send(encodeControlFrame({ type: "pong" }))
        }
        return
      }
      process.stdout.write(new Uint8Array(event.data as ArrayBuffer))
    }
    socket.onerror = () => {
      finish({
        type: "retryable",
        reason: `cannot connect to ${url.host}`,
        livedMillis: openedAt === undefined ? 0 : Date.now() - openedAt,
      })
      socket.terminate()
    }
    socket.onclose = (event) => {
      const livedMillis = openedAt === undefined ? 0 : Date.now() - openedAt
      finish(classifyClose(event.code, event.reason, input.isDetached(), livedMillis))
    }

    return Effect.sync(() => {
      if (done) return
      done = true
      input.clearSocket(socket)
      socket.onopen = null
      socket.onmessage = null
      socket.onerror = null
      socket.onclose = null
      if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) {
        socket.close(1000, CLOSE_DETACH)
      }
    })
  })

const backoffMillis = (attempt: number): number =>
  Math.min(8_000, 500 * 2 ** Math.max(0, Math.min(attempt, 4)))

const errorTag = (error: unknown): string | undefined =>
  typeof error === "object" && error !== null && "_tag" in error && typeof error._tag === "string"
    ? error._tag
    : undefined

const terminalIsLive = (
  server: AppServer.AppServer["Service"],
  terminalId: string,
): Effect.Effect<boolean | undefined> =>
  server.getTerminal(terminalId).pipe(
    Effect.map((terminal) => terminal.status === "live"),
    Effect.catch((error) =>
      Effect.succeed(errorTag(error) === "TerminalNotFound" ? false : undefined),
    ),
  )

const runLoop = (
  server: AppServer.AppServer["Service"],
  terminalId: string,
  url: URL,
  input: AttachInput,
  attempt: number,
): Effect.Effect<AttachOutcome> =>
  Effect.suspend(() => {
    if (input.isDetached()) return Effect.succeed({ type: "detached" } as const)

    return runSocket(url, server.config, input).pipe(
      Effect.flatMap((ended) => {
        if (ended.type === "detached") {
          return Effect.succeed({ type: "detached" } as const)
        }
        if (ended.type === "terminalEnded") {
          return Effect.succeed({ type: "terminalEnded" } as const)
        }

        return Effect.race(
          terminalIsLive(server, terminalId).pipe(
            Effect.map((live) => ({ type: "checked" as const, live })),
          ),
          input.waitForDetach().pipe(Effect.as({ type: "detached" as const })),
        ).pipe(
          Effect.flatMap((checked) => {
            if (checked.type === "detached") {
              return Effect.succeed({ type: "detached" } as const)
            }
            if (checked.live === false) {
              return Effect.succeed({ type: "terminalEnded" } as const)
            }

            const nextAttempt = ended.livedMillis >= 8_000 ? 0 : attempt + 1
            const delay = backoffMillis(attempt)
            return Console.error(
              `\r\natc-tui: connection lost (${ended.reason}); reconnecting in ${delay}ms (Ctrl-\\ to detach)`,
            ).pipe(
              Effect.andThen(
                Effect.race(
                  Effect.sleep(`${delay} millis`).pipe(Effect.as("retry" as const)),
                  input.waitForDetach().pipe(Effect.as("detached" as const)),
                ),
              ),
              Effect.flatMap((choice) =>
                choice === "detached"
                  ? Effect.succeed({ type: "detached" } as const)
                  : runLoop(server, terminalId, url, input, nextAttempt),
              ),
            )
          }),
        )
      }),
    )
  })

const run = (
  server: AppServer.AppServer["Service"],
  terminal: AppServer.Terminal,
): Effect.Effect<AttachOutcome> =>
  Effect.scoped(
    Effect.gen(function* () {
      const detachedQueue = yield* Queue.sliding<void>(1)
      const input = yield* Effect.acquireRelease(
        Effect.sync(() => new AttachInput(detachedQueue).start()),
        (owned) => Effect.sync(() => owned.stop()),
      )
      const size =
        process.stdout.isTTY === true
          ? { cols: process.stdout.columns, rows: process.stdout.rows }
          : undefined
      return yield* runLoop(
        server,
        terminal.id,
        attachUrl(server.config.endpoint, terminal.id, size),
        input,
        0,
      )
    }),
  )

export class Attachment extends Context.Service<
  Attachment,
  {
    readonly run: (terminal: AppServer.Terminal) => Effect.Effect<AttachOutcome>
  }
>()("atc-tui/Attachment") {}

const make = Effect.gen(function* () {
  const server = yield* AppServer.AppServer
  return Attachment.of({ run: (terminal) => run(server, terminal) })
})

export const layer = Layer.effect(Attachment)(make)
