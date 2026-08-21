import { Effect, Option, Ref, Schema, Stream } from "effect"
import type * as Scope from "effect/Scope"
import type * as Contract from "../../app-server/src/api/contract.ts"
import { ResourceChangedEvent } from "../../app-server/src/api/contract.ts"
import type * as Config from "./config.ts"

// Reconnecting consumer for the public resource-change SSE stream. The
// opening comment is surfaced before any refetch, heartbeats feed the silence
// watchdog, and each connection owns its AbortController through a Scope.

export type ResourceChange = typeof Contract.ResourceChangedEvent.Type

export type ResourceSignal =
  | { readonly type: "connected" }
  | { readonly type: "change"; readonly change: ResourceChange }
  | { readonly type: "disconnected"; readonly reason?: string | undefined }

type ParsedItem =
  | { readonly type: "comment"; readonly value: string }
  | { readonly type: "data"; readonly value: string }

type ConnectionSignal =
  | { readonly type: "connected" }
  | { readonly type: "heartbeat" }
  | { readonly type: "change"; readonly change: ResourceChange }

export class SseError extends Schema.TaggedErrorClass<SseError>()("SseError", {
  message: Schema.String,
}) {}

const decodeChange = Schema.decodeUnknownOption(ResourceChangedEvent)

export class SseParser {
  private readonly lineBuffer: Array<number> = []
  private readonly pendingData: Array<string> = []

  consume(bytes: Uint8Array): ReadonlyArray<ParsedItem> {
    const items: Array<ParsedItem> = []
    for (const byte of bytes) {
      if (byte !== 0x0a) {
        this.lineBuffer.push(byte)
        continue
      }
      if (this.lineBuffer.at(-1) === 0x0d) this.lineBuffer.pop()
      const line = new TextDecoder().decode(new Uint8Array(this.lineBuffer))
      this.lineBuffer.length = 0
      const item = this.consumeLine(line)
      if (item !== undefined) items.push(item)
    }
    return items
  }

  private consumeLine(line: string): ParsedItem | undefined {
    if (line === "") {
      if (this.pendingData.length === 0) return undefined
      const value = this.pendingData.join("\n")
      this.pendingData.length = 0
      return { type: "data", value }
    }
    if (line.startsWith(":")) {
      return { type: "comment", value: stripFieldSpace(line.slice(1)) }
    }

    const colon = line.indexOf(":")
    const field = colon === -1 ? line : line.slice(0, colon)
    const value = colon === -1 ? "" : line.slice(colon + 1)
    if (field === "data") this.pendingData.push(stripFieldSpace(value))
    return undefined
  }
}

const stripFieldSpace = (value: string): string => (value.startsWith(" ") ? value.slice(1) : value)

const parseConnectionSignal = (item: ParsedItem): ConnectionSignal | undefined => {
  if (item.type === "comment") {
    if (item.value === "connected") return { type: "connected" }
    return { type: "heartbeat" }
  }

  try {
    const decoded = decodeChange(JSON.parse(item.value))
    return Option.isSome(decoded) ? { type: "change", change: decoded.value } : undefined
  } catch {
    return undefined
  }
}

const headers = (config: Config.ClientConfig["Service"]): Record<string, string> => ({
  accept: "text/event-stream",
  ...(config.token === undefined ? {} : { authorization: `Bearer ${config.token}` }),
})

const connection = (
  config: Config.ClientConfig["Service"],
): Stream.Stream<ConnectionSignal, SseError, Scope.Scope> =>
  Stream.unwrap(
    Effect.gen(function* () {
      const controller = yield* Effect.acquireRelease(
        Effect.sync(() => new AbortController()),
        (owned) => Effect.sync(() => owned.abort()),
      )
      const url = new URL("/api/v1/events", config.endpoint)
      const response = yield* Effect.tryPromise({
        try: () => fetch(url, { headers: headers(config), signal: controller.signal }),
        catch: (error) =>
          new SseError({
            message: error instanceof Error ? error.message : String(error),
          }),
      })
      if (!response.ok) {
        return yield* Effect.fail(
          new SseError({ message: `event stream returned HTTP ${response.status}` }),
        )
      }
      const body = response.body
      if (body === null) {
        return yield* Effect.fail(new SseError({ message: "event stream returned no body" }))
      }

      return Stream.fromReadableStream({
        evaluate: () => body,
        onError: (error) =>
          new SseError({
            message: error instanceof Error ? error.message : String(error),
          }),
      }).pipe(
        Stream.mapAccum(
          () => new SseParser(),
          (parser, chunk) => [
            parser,
            parser
              .consume(chunk)
              .map(parseConnectionSignal)
              .filter((signal): signal is ConnectionSignal => signal !== undefined),
          ],
        ),
        Stream.timeoutOrElse({
          duration: "60 seconds",
          orElse: () => Stream.fail(new SseError({ message: "event stream became silent" })),
        }),
      )
    }),
  )

export const backoffMillis = (attempt: number): number =>
  Math.min(8_000, 500 * 2 ** Math.max(0, Math.min(attempt, 4)))

const runConnection = (
  config: Config.ClientConfig["Service"],
  publish: (signal: ResourceSignal) => Effect.Effect<void>,
): Effect.Effect<never, SseError> =>
  Effect.gen(function* () {
    const connected = yield* Ref.make(false)
    const consume = Effect.scoped(
      connection(config).pipe(
        Stream.runForEach((signal) => {
          if (signal.type === "connected") {
            return Ref.set(connected, true).pipe(Effect.andThen(publish({ type: "connected" })))
          }
          if (signal.type === "change") {
            return publish({ type: "change", change: signal.change })
          }
          return Effect.void
        }),
        Effect.andThen(Effect.fail(new SseError({ message: "event stream ended" }))),
      ),
    )
    return yield* consume.pipe(
      Effect.ensuring(
        Ref.get(connected).pipe(
          Effect.flatMap((wasConnected) =>
            wasConnected ? publish({ type: "disconnected" }) : Effect.void,
          ),
        ),
      ),
    )
  })

export const subscribe = (
  config: Config.ClientConfig["Service"],
  publish: (signal: ResourceSignal) => Effect.Effect<void>,
): Effect.Effect<never> => {
  const loop = (attempt: number): Effect.Effect<never> =>
    runConnection(config, publish).pipe(
      Effect.catch((error) =>
        publish({ type: "disconnected", reason: error.message }).pipe(
          Effect.andThen(Effect.sleep(`${backoffMillis(attempt)} millis`)),
          Effect.andThen(Effect.suspend(() => loop(attempt + 1))),
        ),
      ),
    )
  return loop(0)
}
