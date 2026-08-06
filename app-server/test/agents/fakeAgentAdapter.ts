import { Cause, Effect, Queue, Stream } from "effect"
import type {
  AgentActivity,
  AgentAdapter,
  AgentConnection,
  AgentEvent,
  AgentRequestKind,
  AgentTurn,
  AgentTurnOutcome,
} from "../../src/agents/agentAdapter.ts"
import {
  AgentConflict,
  AgentIdentityMismatch,
  AgentProtocolError,
  AgentResumeFailed,
  AgentUnavailable,
} from "../../src/agents/agentAdapter.ts"

// The fake-adapter test seam (ATC-123): an in-memory AgentAdapter with the
// same observable semantics the real adapters must hold — create and resume
// are distinct, resume fails closed on unknown ids and mismatched cwd, one
// active writer per session, one active turn per connection, stale interrupt
// targets rejected — plus controls to script turn outcomes and provider
// requests and switches to inject unavailability.
//
// It models the EAGER-verification, connection-survives-failed-turns flavor
// (Codex). Claude differs on both axes (verification at first turn,
// connection ends after non-success turns) — consumers exercising those
// paths must test against the real adapters' fixtures, not this fake.

export interface FakeAgentSession {
  readonly providerSessionId: string
  readonly cwd: string
  /** Inputs received via createSession/startTurn, for assertions. */
  readonly inputs: Array<string>
}

export interface FakeAgentAdapter {
  readonly adapter: AgentAdapter
  readonly sessions: Map<string, FakeAgentSession>
  /** While set, every operation fails with AgentUnavailable(reason). */
  readonly setUnavailable: (reason: string | null) => void
  /** Insert a resumable session directly (no createSession). */
  readonly seed: (providerSessionId: string, cwd: string) => FakeAgentSession
  /** Complete the active turn of `providerSessionId` with `outcome`. */
  readonly completeTurn: (providerSessionId: string, outcome: AgentTurnOutcome) => void
  /** Open a provider request on the active turn; activity → needs_input. */
  readonly openRequest: (providerSessionId: string, kind: AgentRequestKind) => string
  /** Close a previously opened request; activity → working. */
  readonly closeRequest: (providerSessionId: string, requestId: string) => void
}

interface LiveConnection {
  readonly events: Queue.Queue<AgentEvent, Cause.Done>
  activity: AgentActivity
  activeTurn: string | null
  closed: boolean
  readonly openRequests: Set<string>
}

export const makeFakeAgentAdapter = (): FakeAgentAdapter => {
  const sessions = new Map<string, FakeAgentSession>()
  // One live connection per session — the single-writer rule.
  const connections = new Map<string, LiveConnection>()
  let unavailableReason: string | null = null
  let nextSessionId = 1
  let nextTurnId = 1
  let nextRequestId = 1

  const requireAvailable = Effect.suspend(() =>
    unavailableReason !== null
      ? Effect.fail(new AgentUnavailable({ provider: "codex", reason: unavailableReason }))
      : Effect.void,
  )

  const emit = (live: LiveConnection, event: AgentEvent): void => {
    Queue.offerUnsafe(live.events, event)
  }

  const setActivity = (live: LiveConnection, activity: AgentActivity): void => {
    live.activity = activity
    emit(live, { type: "activity", activity })
  }

  const requireLive = (providerSessionId: string): LiveConnection => {
    const live = connections.get(providerSessionId)
    if (live === undefined) {
      throw new Error(`fake adapter: no live connection for ${providerSessionId}`)
    }
    return live
  }

  const openConnection = (session: FakeAgentSession) =>
    Effect.gen(function* () {
      if (connections.has(session.providerSessionId)) {
        return yield* Effect.fail(
          new AgentConflict({
            provider: "codex",
            reason: `a writer is already connected to ${session.providerSessionId}`,
          }),
        )
      }
      const events = yield* Queue.make<AgentEvent, Cause.Done>()
      const live: LiveConnection = {
        events,
        activity: "idle",
        activeTurn: null,
        closed: false,
        openRequests: new Set(),
      }
      connections.set(session.providerSessionId, live)
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => {
          live.closed = true
          connections.delete(session.providerSessionId)
          Queue.endUnsafe(events)
        }),
      )

      const requireOpen = Effect.suspend(() =>
        live.closed
          ? Effect.fail(
              new AgentConflict({ provider: "codex", reason: "the connection is closed" }),
            )
          : Effect.void,
      )

      const startTurn = (input: string) =>
        Effect.gen(function* () {
          yield* requireAvailable
          yield* requireOpen
          if (live.activeTurn !== null) {
            return yield* Effect.fail(
              new AgentConflict({
                provider: "codex",
                reason: `turn ${live.activeTurn} is still active`,
              }),
            )
          }
          session.inputs.push(input)
          const turnId = `fake-turn-${nextTurnId++}`
          live.activeTurn = turnId
          emit(live, { type: "turnStarted", turnId })
          setActivity(live, "working")
          return { turnId }
        })

      const connection: AgentConnection = {
        providerSessionId: session.providerSessionId,
        cwd: session.cwd,
        activity: Effect.sync(() => live.activity),
        events: Stream.fromQueue(events).pipe(
          Stream.mapError(
            (cause) => new AgentProtocolError({ provider: "codex", reason: String(cause) }),
          ),
        ),
        startTurn,
        interrupt: (turn: AgentTurn) =>
          Effect.gen(function* () {
            yield* requireAvailable
            yield* requireOpen
            if (live.activeTurn !== turn.turnId) {
              return yield* Effect.fail(
                new AgentConflict({
                  provider: "codex",
                  reason: `turn ${turn.turnId} is not the active turn`,
                }),
              )
            }
            finishTurn(session.providerSessionId, "interrupted")
          }),
      }
      return connection
    })

  const finishTurn = (providerSessionId: string, outcome: AgentTurnOutcome): void => {
    const live = requireLive(providerSessionId)
    if (live.activeTurn === null) {
      throw new Error(`fake adapter: no active turn on ${providerSessionId}`)
    }
    const turnId = live.activeTurn
    live.activeTurn = null
    for (const requestId of live.openRequests) {
      emit(live, { type: "requestClosed", requestId })
    }
    live.openRequests.clear()
    emit(live, { type: "turnCompleted", turnId, outcome })
    setActivity(live, "idle")
  }

  const adapter: AgentAdapter = {
    provider: "codex",
    capabilities: { testedVersion: "0.0.0-fake", tuiObservation: "shared-server" },
    createSession: (options) =>
      Effect.gen(function* () {
        yield* requireAvailable
        const session: FakeAgentSession = {
          providerSessionId: `fake-session-${nextSessionId++}`,
          cwd: options.cwd,
          inputs: [],
        }
        sessions.set(session.providerSessionId, session)
        const connection = yield* openConnection(session)
        // The fake verifies eagerly, so deferred-verification tags cannot
        // occur on its first turn.
        const turn = yield* connection
          .startTurn(options.input)
          .pipe(Effect.catchTag("AgentResumeFailed", (error) => Effect.die(error)))
        return { connection, turn }
      }),
    resumeSession: (options) =>
      Effect.gen(function* () {
        yield* requireAvailable
        const session = sessions.get(options.providerSessionId)
        if (session === undefined) {
          return yield* Effect.fail(
            new AgentResumeFailed({
              provider: "codex",
              providerSessionId: options.providerSessionId,
              reason: "no session found for id",
            }),
          )
        }
        if (session.cwd !== options.cwd) {
          return yield* Effect.fail(
            new AgentIdentityMismatch({
              provider: "codex",
              field: "cwd",
              expected: options.cwd,
              actual: session.cwd,
            }),
          )
        }
        return yield* openConnection(session)
      }),
    tuiLaunch: (options) =>
      requireAvailable.pipe(
        Effect.as({
          command: ["fake-agent", "resume", options.providerSessionId],
          env: {},
        }),
      ),
  }

  return {
    adapter,
    sessions,
    setUnavailable: (reason) => {
      unavailableReason = reason
    },
    seed: (providerSessionId, cwd) => {
      const session: FakeAgentSession = { providerSessionId, cwd, inputs: [] }
      sessions.set(providerSessionId, session)
      return session
    },
    completeTurn: (providerSessionId, outcome) => finishTurn(providerSessionId, outcome),
    openRequest: (providerSessionId, kind) => {
      const live = requireLive(providerSessionId)
      const requestId = `fake-request-${nextRequestId++}`
      live.openRequests.add(requestId)
      emit(live, { type: "requestOpened", requestId, kind })
      setActivity(live, "needs_input")
      return requestId
    },
    closeRequest: (providerSessionId, requestId) => {
      const live = requireLive(providerSessionId)
      if (!live.openRequests.delete(requestId)) {
        throw new Error(`fake adapter: request ${requestId} is not open`)
      }
      emit(live, { type: "requestClosed", requestId })
      setActivity(live, "working")
    },
  }
}
