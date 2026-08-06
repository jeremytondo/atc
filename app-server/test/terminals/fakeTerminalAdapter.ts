import { Cause, Effect, Layer, Queue, Stream } from "effect"
import { ZmxUnavailable } from "../../src/api/contract.ts"
import { SubprocessError } from "../../src/platform/subprocess.ts"
import type {
  SessionConnection,
  SessionInfo,
  SessionSize,
} from "../../src/terminals/terminalAdapter.ts"
import {
  SessionNotFound,
  SessionOperationFailed,
  TerminalAdapter,
} from "../../src/terminals/terminalAdapter.ts"

// The fake-adapter test seam (ATC-122): an in-memory TerminalAdapter with
// the same observable semantics as the zmx adapter — inventory-authoritative
// absence, create-never-attaches, idempotent kill, pre-flighted attach that
// refuses unreachable sessions — plus switches to inject the failures
// reconciliation must survive.

export interface FakeSession {
  readonly name: string
  readonly cwd: string
  readonly command: ReadonlyArray<string> | undefined
  /** The per-session env overlay the session was created with. */
  readonly env: Record<string, string | undefined> | undefined
  /** Everything written to an attached connection, for assertions. */
  readonly written: Array<Uint8Array | string>
  lastResize: SessionSize | undefined
  /** Flip to false to model an existing-but-unresponsive daemon. */
  reachable: boolean
  /** Flip to true to model a dead attach-client PTY: writes fail. */
  writeFails: boolean
}

export interface FakeTerminalAdapter {
  readonly layer: Layer.Layer<TerminalAdapter>
  readonly sessions: Map<string, FakeSession>
  /** While true, every inventory-consulting operation is ZmxUnavailable. */
  readonly setUnavailable: (unavailable: boolean) => void
  /** While true, createSession fails conclusively (a failed launch). */
  readonly setCreateFails: (fails: boolean) => void
  /** Emit output bytes to every open connection of `name`. */
  readonly emitOutput: (name: string, data: Uint8Array) => void
  /** Insert a session directly (no createSession), e.g. recovery seeding. */
  readonly seed: (name: string, overrides?: Partial<Omit<FakeSession, "name">>) => FakeSession
}

export const makeFakeAdapter = (): FakeTerminalAdapter => {
  const sessions = new Map<string, FakeSession>()
  const connections = new Map<string, Set<Queue.Queue<Uint8Array, Cause.Done>>>()
  let unavailable = false
  let createFails = false

  const requireAvailable = Effect.suspend(() =>
    unavailable
      ? Effect.fail(new ZmxUnavailable({ reason: "fake adapter set unavailable" }))
      : Effect.void,
  )

  const service: TerminalAdapter["Service"] = {
    createSession: (options) =>
      Effect.gen(function* () {
        yield* requireAvailable
        if (createFails) {
          return yield* Effect.fail(
            new SessionOperationFailed({
              operation: "create",
              sessionName: options.name,
              reason: "fake adapter set to fail creation",
            }),
          )
        }
        if (sessions.has(options.name)) {
          return yield* Effect.fail(
            new SessionOperationFailed({
              operation: "create",
              sessionName: options.name,
              reason: "session already exists",
            }),
          )
        }
        sessions.set(options.name, {
          name: options.name,
          cwd: options.cwd,
          command: options.command,
          env: options.env,
          written: [],
          lastResize: undefined,
          reachable: true,
          writeFails: false,
        })
      }),
    listSessions: () =>
      requireAvailable.pipe(
        Effect.map((): Array<SessionInfo> =>
          [...sessions.values()].map((session) => ({
            name: session.name,
            reachable: session.reachable,
          })),
        ),
      ),
    killSession: (name) =>
      requireAvailable.pipe(
        Effect.map(() => {
          sessions.delete(name)
          for (const queue of connections.get(name) ?? []) Queue.endUnsafe(queue)
          connections.delete(name)
        }),
      ),
    attachSession: (name, size) =>
      Effect.gen(function* () {
        yield* requireAvailable
        const session = sessions.get(name)
        if (session === undefined) {
          return yield* Effect.fail(new SessionNotFound({ sessionName: name }))
        }
        if (!session.reachable) {
          return yield* Effect.fail(
            new SessionOperationFailed({
              operation: "attach",
              sessionName: name,
              reason: "session exists but is unreachable; retry",
            }),
          )
        }
        session.lastResize = size
        const output = yield* Queue.make<Uint8Array, Cause.Done>()
        const open = connections.get(name) ?? new Set()
        open.add(output)
        connections.set(name, open)
        yield* Effect.addFinalizer(() =>
          Effect.sync(() => {
            open.delete(output)
            Queue.endUnsafe(output)
          }),
        )
        const connection: SessionConnection = {
          output: Stream.fromQueue(output),
          write: (data) =>
            Effect.suspend(() =>
              session.writeFails
                ? Effect.fail(
                    new SubprocessError({
                      executable: "fake-zmx",
                      operation: "stdin",
                      message: "terminal is closed (fake writeFails)",
                    }),
                  )
                : Effect.sync(() => {
                    session.written.push(data)
                  }),
            ),
          resize: (next) =>
            Effect.sync(() => {
              session.lastResize = next
            }),
          exitCode: Effect.never,
        }
        return connection
      }),
  }

  return {
    layer: Layer.succeed(TerminalAdapter)(service),
    sessions,
    setUnavailable: (value) => {
      unavailable = value
    },
    setCreateFails: (value) => {
      createFails = value
    },
    emitOutput: (name, data) => {
      for (const queue of connections.get(name) ?? []) Queue.offerUnsafe(queue, data)
    },
    seed: (name, overrides) => {
      const session: FakeSession = {
        name,
        cwd: "/",
        command: undefined,
        env: undefined,
        written: [],
        lastResize: undefined,
        reachable: true,
        writeFails: false,
        ...overrides,
      }
      sessions.set(name, session)
      return session
    },
  }
}
