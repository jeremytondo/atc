import { Cause, Deferred, Effect, Queue, Stream } from "effect"
import type {
  AgentActivity,
  AgentAdapter,
  AgentCommand,
  AgentConnection,
  AgentEvent,
  AgentModel,
  AgentSessionEvent,
  AgentTurn,
  AgentTurnOutcome,
  HistoryTurn,
  ProviderSettings,
  ThreadAttachment,
  ThreadItem,
  ThreadRequest,
  ThreadRequestAnswer,
  ThreadSettings,
  TurnInput,
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
// targets rejected, provider requests parked until answered — plus controls
// to script turn outcomes, items, requests, and history, and switches to
// inject unavailability.
//
// It models the EAGER-verification, connection-survives-failed-turns flavor
// (Codex). Claude differs on both axes (verification at first turn,
// connection ends after non-success turns) — consumers exercising those
// paths must test against the real adapters' fixtures, not this fake;
// `endConnection` scripts a provider-side end of the connection so a
// consumer can still stage that shape (a non-success turn, then the feed
// ending) against the fake.

export interface FakeAgentSession {
  readonly providerSessionId: string
  readonly cwd: string
  /** Input texts received via createSession/startTurn, for assertions. */
  readonly inputs: Array<string>
  /** The attachments each input carried, aligned with `inputs`. */
  readonly attachments: Array<ReadonlyArray<ThreadAttachment>>
  /** Settings each turn was started with (createSession/startTurn), in order. */
  readonly turnSettings: Array<ThreadSettings>
}

export interface FakeAgentAdapter {
  readonly adapter: AgentAdapter
  readonly sessions: Map<string, FakeAgentSession>
  /** While set, every operation fails with AgentUnavailable(reason). */
  readonly setUnavailable: (reason: string | null) => void
  /** Insert a resumable session directly (no createSession). */
  readonly seed: (providerSessionId: string, cwd: string) => FakeAgentSession
  /** Make the next resume of `providerSessionId` find turn `turnId` still
   * running (working) — a shared provider server across an ATC restart. */
  readonly seedRunningTurn: (providerSessionId: string, turnId: string) => void
  /** Complete the active turn of `providerSessionId` with `outcome`. */
  readonly completeTurn: (providerSessionId: string, outcome: AgentTurnOutcome) => void
  /** The provider starts a turn by itself on the live connection (a Claude
   * root loop woken by a finished background task): turnStarted and the
   * notification as its first userMessage, no startTurn. Returns the turn id;
   * `completeTurn` ends it like any other. */
  readonly startProviderTurn: (providerSessionId: string, text: string) => string
  /** End the live connection of `providerSessionId` from the provider's
   * side (its feed ends cleanly, control calls refuse) — a Claude session
   * ending after a non-success turn, a resident child that exited. */
  readonly endConnection: (providerSessionId: string) => void
  /** Whether `providerSessionId` has a live writer connection. */
  readonly isConnected: (providerSessionId: string) => boolean
  /** Push one activity value onto the live connection's feed (session-level
   * evidence between turns: background work winding down, then idle). */
  readonly emitConnectionActivity: (providerSessionId: string, activity: AgentActivity) => void
  /** Writer connections opened (createSession/resumeSession), in order. */
  readonly connectionsOpened: Array<string>
  /** Open a stock provider request (an approval, or a one-question ask)
   * on the active turn; activity → needs_input. */
  readonly openRequest: (providerSessionId: string, kind: "approval" | "question") => string
  /** Close a previously opened request without an answer; activity → working. */
  readonly closeRequest: (providerSessionId: string, requestId: string) => void
  /** Answers `respond` delivered, in order. */
  readonly answers: Array<{ requestId: string; answer: ThreadRequestAnswer }>
  /** Script what listCommands returns (default []). */
  readonly setCommands: (commands: ReadonlyArray<AgentCommand>) => void
  /** listCommands calls (cwds), in order. */
  readonly commandReads: Array<string>
  /** Push one item event onto the active connection's feed. */
  readonly emitItem: (
    providerSessionId: string,
    phase: "itemStarted" | "itemUpdated" | "itemCompleted",
    item: ThreadItem,
  ) => void
  /** Push one text delta onto the active connection's feed. */
  readonly emitTextDelta: (providerSessionId: string, itemId: string, delta: string) => void
  /** Script what readHistory returns for a session (default []). */
  readonly setHistory: (providerSessionId: string, turns: ReadonlyArray<HistoryTurn>) => void
  /** readHistory calls (providerSessionIds), in order. */
  readonly historyReads: Array<string>
  /** While set, prepared identities wait on a gate; unsetting releases any
   * waiter (so a hung identity can resolve late, after the test acted). */
  readonly setIdentityHangs: (hangs: boolean) => void
  /** Push one activity value to every observer of `providerSessionId`. */
  readonly emitActivity: (providerSessionId: string, activity: AgentActivity) => void
  /** Push one userPrompt event to every observer of `providerSessionId`. */
  readonly emitUserPrompt: (providerSessionId: string, text: string) => void
  /** generateTitle calls observed, in order. */
  readonly titleRequests: Array<{
    cwd: string
    prompt: string
    refine?: { context: string; currentTitle: string | null }
  }>
  /** generateTitle outcomes (the raw title or the failure), in order —
   * the deterministic completion signal for race tests. */
  readonly titleOutcomes: Array<{ title?: string; error?: string }>
  /** Script what generateTitle returns (default "Fake generated title"). */
  readonly setTitle: (title: string) => void
  /** collectTitleContext calls (providerSessionIds), in order. */
  readonly contextRequests: Array<string>
  /** Script what collectTitleContext returns for a session (default null). */
  readonly setTitleContext: (providerSessionId: string, context: string | null) => void
  /** While set, generateTitle fails with AgentProtocolError(reason). */
  readonly setTitleFails: (reason: string | null) => void
  /** While set, generateTitle waits on a gate; unsetting releases waiters. */
  readonly setTitleHangs: (hangs: boolean) => void
  /** Live observeSession subscriptions for `providerSessionId`. */
  readonly observerCount: (providerSessionId: string) => number
  /** End every live observation feed for `providerSessionId` — models an
   * evidence source dying out from under its subscribers. */
  readonly endObservations: (providerSessionId: string) => void
  /** Script what checkSession reports for `providerSessionId`. */
  readonly setCheckActivity: (providerSessionId: string, activity: AgentActivity | null) => void
  /** checkSession calls (providerSessionIds), in order. */
  readonly checkCalls: Array<string>
  /** While set, checkSession waits on a gate; unsetting releases waiters. */
  readonly setCheckHangs: (hangs: boolean) => void
  /** High-water mark of concurrent checkSession calls. */
  readonly maxConcurrentChecks: () => number
  /** Zero the checkSession log and concurrency high-water mark. */
  readonly resetCheckStats: () => void
  /** Mark a session as pruned provider-side: tuiLaunch's existence probe
   * fails AgentResumeFailed (the Codex reopen-probe behavior). */
  readonly setSessionPruned: (providerSessionId: string, pruned: boolean) => void
  /** prepareTuiSession identities handed out, in order. */
  readonly prepared: Array<{ providerSessionId: string; cwd: string }>
  /** releaseSession calls, in order. */
  readonly released: Array<{ providerSessionId: string; providerMetadata: string | undefined }>
  /** Push one provider settings report onto the live connection's feed and
   * to every observer of `providerSessionId` (a change made in the TUI). */
  readonly emitSettings: (providerSessionId: string, settings: ProviderSettings) => void
  /** Script what listModels returns (default: a two-model catalog). */
  readonly setModels: (models: ReadonlyArray<AgentModel>) => void
  /** listModels calls so far. */
  readonly modelReads: () => number
  /** Park every listModels (after it is counted) on `gate` — a slow
   * catalog read, so a caller can be caught between reading a row and
   * writing it. `Effect.void` restores the instant answer. */
  readonly gateModels: (gate: Effect.Effect<void>) => void
}

export interface FakeAgentAdapterOptions {
  /** Adapter's observation-authority shape (default true — codex-like). */
  readonly sharedServer?: boolean
}

interface LiveConnection {
  readonly events: Queue.Queue<AgentEvent, Cause.Done>
  activity: AgentActivity
  activeTurn: string | null
  closed: boolean
  readonly openRequests: Map<string, ThreadRequest>
}

export const makeFakeAgentAdapter = (options: FakeAgentAdapterOptions = {}): FakeAgentAdapter => {
  const sessions = new Map<string, FakeAgentSession>()
  // One live connection per session — the single-writer rule.
  const connections = new Map<string, LiveConnection>()
  const observers = new Map<string, Set<Queue.Queue<AgentSessionEvent, Cause.Done>>>()
  const checkActivity = new Map<string, AgentActivity>()
  const runningOnResume = new Map<string, string>()
  let commands: ReadonlyArray<AgentCommand> = []
  const commandReads: Array<string> = []
  const histories = new Map<string, ReadonlyArray<HistoryTurn>>()
  const historyReads: Array<string> = []
  const answers: FakeAgentAdapter["answers"] = []
  const prunedSessions = new Set<string>()
  const prepared: FakeAgentAdapter["prepared"] = []
  const connectionsOpened: FakeAgentAdapter["connectionsOpened"] = []
  const released: FakeAgentAdapter["released"] = []
  const titleRequests: FakeAgentAdapter["titleRequests"] = []
  const titleOutcomes: FakeAgentAdapter["titleOutcomes"] = []
  const contextRequests: FakeAgentAdapter["contextRequests"] = []
  const titleContexts = new Map<string, string>()
  let scriptedTitle = "Fake generated title"
  let titleFailsReason: string | null = null
  let titleGate: Deferred.Deferred<void> | null = null
  let identityGate: Deferred.Deferred<void> | null = null
  let checkGate: Deferred.Deferred<void> | null = null
  const checkCalls: FakeAgentAdapter["checkCalls"] = []
  let inFlightChecks = 0
  let maxInFlightChecks = 0
  let unavailableReason: string | null = null
  let models: ReadonlyArray<AgentModel> = [
    {
      value: "fake-large",
      displayName: "Fake Large",
      description: "The default fake model",
      isDefault: true,
      supportedEffortLevels: ["low", "medium", "high"],
      defaultEffortLevel: "medium",
    },
    {
      value: "fake-small",
      displayName: "Fake Small",
      description: "No effort support",
      isDefault: false,
      supportedEffortLevels: [],
    },
  ]
  let modelReads = 0
  let modelsGate: Effect.Effect<void> = Effect.void
  let nextSessionId = 1
  let nextTurnId = 1
  const instance = crypto.randomUUID().slice(0, 8)
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

  /** The connection ends (its scope closed, or the provider ended it):
   * control calls refuse, the feed ends cleanly, the writer slot frees. */
  const closeConnection = (providerSessionId: string, live: LiveConnection): void => {
    live.closed = true
    if (connections.get(providerSessionId) === live) connections.delete(providerSessionId)
    Queue.endUnsafe(live.events)
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
      const running = runningOnResume.get(session.providerSessionId) ?? null
      runningOnResume.delete(session.providerSessionId)
      const live: LiveConnection = {
        events,
        activity: running === null ? "idle" : "working",
        activeTurn: running,
        closed: false,
        openRequests: new Map(),
      }
      connections.set(session.providerSessionId, live)
      connectionsOpened.push(session.providerSessionId)
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => closeConnection(session.providerSessionId, live)),
      )

      const requireOpen = Effect.suspend(() =>
        live.closed
          ? Effect.fail(
              new AgentConflict({ provider: "codex", reason: "the connection is closed" }),
            )
          : Effect.void,
      )

      const startTurn = (input: TurnInput, settings: ThreadSettings) =>
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
          session.inputs.push(input.text)
          session.attachments.push(input.attachments)
          session.turnSettings.push(settings)
          // Unique across fake instances: turn ids are persisted, and a
          // "restarted" fake must not collide with the previous one's turns.
          const turnId = `fake-turn-${nextTurnId++}-${instance}`
          live.activeTurn = turnId
          emit(live, { type: "turnStarted", turnId })
          // Both real adapters put the prompt on the feed as the turn's
          // first item (Codex via the fan-out, Claude explicitly).
          emit(live, {
            type: "itemCompleted",
            item: {
              type: "userMessage",
              id: `${turnId}:prompt`,
              turnId,
              text: input.text,
              ...(input.attachments.length > 0 ? { attachments: input.attachments } : {}),
            },
          })
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
        steer: (input, turn) =>
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
            session.inputs.push(input.text)
            session.attachments.push(input.attachments)
            // Both real adapters surface the message as the turn's next item.
            emit(live, {
              type: "itemCompleted",
              item: {
                type: "userMessage",
                id: `${turn.turnId}:steer-${session.inputs.length}`,
                turnId: turn.turnId,
                text: input.text,
                ...(input.attachments.length > 0 ? { attachments: input.attachments } : {}),
              },
            })
          }),
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
        respond: (requestId, answer) =>
          Effect.gen(function* () {
            yield* requireAvailable
            yield* requireOpen
            const request = live.openRequests.get(requestId)
            if (request === undefined) {
              return yield* Effect.fail(
                new AgentConflict({
                  provider: "codex",
                  reason: `request ${requestId} is not pending`,
                }),
              )
            }
            if (request.kind !== answer.kind) {
              return yield* Effect.fail(
                new AgentConflict({
                  provider: "codex",
                  reason: `request ${requestId} expects a ${request.kind} answer`,
                }),
              )
            }
            answers.push({ requestId, answer })
            live.openRequests.delete(requestId)
            emit(live, { type: "requestClosed", requestId })
            setActivity(live, "working")
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
    for (const requestId of live.openRequests.keys()) {
      emit(live, { type: "requestClosed", requestId })
    }
    live.openRequests.clear()
    emit(live, { type: "turnCompleted", turnId, outcome })
    setActivity(live, "idle")
  }

  const adapter: AgentAdapter = {
    provider: "codex",
    sharedServer: options.sharedServer ?? true,
    createSession: (options) =>
      Effect.gen(function* () {
        yield* requireAvailable
        const session: FakeAgentSession = {
          providerSessionId: `fake-session-${nextSessionId++}`,
          cwd: options.cwd,
          inputs: [],
          attachments: [],
          turnSettings: [],
        }
        sessions.set(session.providerSessionId, session)
        const connection = yield* openConnection(session)
        // The fake verifies eagerly, so deferred-verification tags cannot
        // occur on its first turn.
        const turn = yield* connection
          .startTurn(options.input, options.settings)
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
        Effect.andThen(() =>
          prunedSessions.has(options.providerSessionId)
            ? Effect.fail(
                new AgentResumeFailed({
                  provider: "codex",
                  providerSessionId: options.providerSessionId,
                  reason: "the provider no longer has this session",
                }),
              )
            : Effect.succeed({
                launchSpec: {
                  command: ["fake-agent", "resume", options.providerSessionId],
                  env: {},
                },
              }),
        ),
      ),
    prepareTuiSession: (options) =>
      Effect.gen(function* () {
        yield* requireAvailable
        const providerSessionId = `fake-session-${nextSessionId++}`
        prepared.push({ providerSessionId, cwd: options.cwd })
        // The prepared session becomes resumable once identity resolves —
        // mirroring "a completed first interaction materializes it".
        const resolve = Effect.sync(() => {
          sessions.set(providerSessionId, {
            providerSessionId,
            cwd: options.cwd,
            inputs: [],
            attachments: [],
            turnSettings: [],
          })
          return {
            providerSessionId,
            cwd: options.cwd,
            providerMetadata: JSON.stringify({ fake: providerSessionId }),
          }
        })
        const identity = Effect.suspend(() => {
          const gate = identityGate
          return gate === null ? resolve : Deferred.await(gate).pipe(Effect.andThen(resolve))
        })
        return {
          launchSpec: {
            command: ["fake-agent", "--fresh", providerSessionId],
            env: { FAKE_AGENT: "1" },
          },
          identity,
        }
      }),
    observeSession: (options) =>
      Effect.gen(function* () {
        yield* requireAvailable
        const queue = yield* Queue.make<AgentSessionEvent, Cause.Done>()
        const set = observers.get(options.providerSessionId) ?? new Set()
        set.add(queue)
        observers.set(options.providerSessionId, set)
        yield* Effect.addFinalizer(() =>
          Effect.sync(() => {
            set.delete(queue)
            if (set.size === 0) observers.delete(options.providerSessionId)
            Queue.endUnsafe(queue)
          }),
        )
        return Stream.fromQueue(queue)
      }),
    listCommands: (options) =>
      requireAvailable.pipe(
        Effect.map(() => {
          commandReads.push(options.cwd)
          return commands
        }),
      ),
    listModels: () =>
      Effect.gen(function* () {
        yield* requireAvailable
        modelReads++
        yield* modelsGate
        return models
      }),
    generateTitle: (options) =>
      Effect.gen(function* () {
        yield* requireAvailable
        titleRequests.push({
          cwd: options.cwd,
          prompt: options.prompt,
          ...(options.refine !== undefined ? { refine: options.refine } : {}),
        })
        const gate = titleGate
        if (gate !== null) yield* Deferred.await(gate)
        if (titleFailsReason !== null) {
          titleOutcomes.push({ error: titleFailsReason })
          return yield* Effect.fail(
            new AgentProtocolError({ provider: "codex", reason: titleFailsReason }),
          )
        }
        titleOutcomes.push({ title: scriptedTitle })
        return scriptedTitle
      }),
    collectTitleContext: (options) =>
      Effect.sync(() => {
        contextRequests.push(options.providerSessionId)
        return titleContexts.get(options.providerSessionId) ?? null
      }),
    readHistory: (options) =>
      Effect.gen(function* () {
        yield* requireAvailable
        historyReads.push(options.providerSessionId)
        return histories.get(options.providerSessionId) ?? []
      }),
    checkSession: (options) =>
      Effect.gen(function* () {
        checkCalls.push(options.providerSessionId)
        inFlightChecks++
        maxInFlightChecks = Math.max(maxInFlightChecks, inFlightChecks)
        const gate = checkGate
        yield* Effect.ensuring(
          gate !== null ? Deferred.await(gate) : Effect.void,
          Effect.sync(() => {
            inFlightChecks--
          }),
        )
        yield* requireAvailable
        return checkActivity.get(options.providerSessionId) ?? "unknown"
      }),
    releaseSession: (options) =>
      Effect.sync(() => {
        released.push({
          providerSessionId: options.providerSessionId,
          providerMetadata: options.providerMetadata,
        })
      }),
  }

  return {
    adapter,
    sessions,
    setUnavailable: (reason) => {
      unavailableReason = reason
    },
    seed: (providerSessionId, cwd) => {
      const session: FakeAgentSession = {
        providerSessionId,
        cwd,
        inputs: [],
        attachments: [],
        turnSettings: [],
      }
      sessions.set(providerSessionId, session)
      return session
    },
    seedRunningTurn: (providerSessionId, turnId) => {
      runningOnResume.set(providerSessionId, turnId)
    },
    completeTurn: (providerSessionId, outcome) => finishTurn(providerSessionId, outcome),
    startProviderTurn: (providerSessionId, text) => {
      const live = requireLive(providerSessionId)
      if (live.activeTurn !== null) {
        throw new Error(`fake adapter: turn ${live.activeTurn} is still active`)
      }
      const turnId = `fake-wake-${nextTurnId++}-${instance}`
      live.activeTurn = turnId
      emit(live, { type: "turnStarted", turnId })
      emit(live, {
        type: "itemCompleted",
        item: { type: "userMessage", id: `${turnId}:prompt`, turnId, text },
      })
      setActivity(live, "working")
      return turnId
    },
    endConnection: (providerSessionId) =>
      closeConnection(providerSessionId, requireLive(providerSessionId)),
    isConnected: (providerSessionId) => connections.has(providerSessionId),
    emitConnectionActivity: (providerSessionId, activity) =>
      setActivity(requireLive(providerSessionId), activity),
    connectionsOpened,
    openRequest: (providerSessionId, kind) => {
      const live = requireLive(providerSessionId)
      const requestId = `fake-request-${nextRequestId++}`
      const base = {
        id: requestId,
        turnId: live.activeTurn ?? "",
        openedAt: new Date().toISOString(),
      }
      const request: ThreadRequest =
        kind === "approval"
          ? { kind, ...base, title: "pwd", subject: { type: "command", command: "pwd" } }
          : {
              kind,
              ...base,
              questions: [
                {
                  id: "q0",
                  header: "Choice",
                  question: "Which one?",
                  options: [
                    { label: "A", description: "first" },
                    { label: "B", description: "second" },
                  ],
                  multiSelect: false,
                  freeform: false,
                  secret: false,
                },
              ],
            }
      live.openRequests.set(requestId, request)
      emit(live, { type: "requestOpened", request })
      setActivity(live, "needs_input")
      return requestId
    },
    answers,
    emitItem: (providerSessionId, phase, item) => {
      emit(requireLive(providerSessionId), { type: phase, item })
    },
    emitTextDelta: (providerSessionId, itemId, delta) => {
      emit(requireLive(providerSessionId), { type: "textDelta", itemId, delta })
    },
    setHistory: (providerSessionId, turns) => {
      histories.set(providerSessionId, turns)
    },
    historyReads,
    setCommands: (next) => {
      commands = next
    },
    commandReads,
    closeRequest: (providerSessionId, requestId) => {
      const live = requireLive(providerSessionId)
      if (!live.openRequests.delete(requestId)) {
        throw new Error(`fake adapter: request ${requestId} is not open`)
      }
      emit(live, { type: "requestClosed", requestId })
      setActivity(live, "working")
    },
    setIdentityHangs: (hangs) => {
      if (hangs) {
        identityGate = Deferred.makeUnsafe<void>()
        return
      }
      if (identityGate !== null) Deferred.doneUnsafe(identityGate, Effect.void)
      identityGate = null
    },
    emitActivity: (providerSessionId, activity) => {
      for (const queue of observers.get(providerSessionId) ?? []) {
        Queue.offerUnsafe(queue, { type: "activity", activity })
      }
    },
    emitUserPrompt: (providerSessionId, text) => {
      for (const queue of observers.get(providerSessionId) ?? []) {
        Queue.offerUnsafe(queue, { type: "userPrompt", text })
      }
    },
    emitSettings: (providerSessionId, settings) => {
      const live = connections.get(providerSessionId)
      if (live !== undefined) emit(live, { type: "settings", settings })
      for (const queue of observers.get(providerSessionId) ?? []) {
        Queue.offerUnsafe(queue, { type: "settings", settings })
      }
    },
    setModels: (next) => {
      models = next
    },
    modelReads: () => modelReads,
    gateModels: (gate) => {
      modelsGate = gate
    },
    titleRequests,
    titleOutcomes,
    setTitle: (title) => {
      scriptedTitle = title
    },
    contextRequests,
    setTitleContext: (providerSessionId, context) => {
      if (context === null) titleContexts.delete(providerSessionId)
      else titleContexts.set(providerSessionId, context)
    },
    setTitleFails: (reason) => {
      titleFailsReason = reason
    },
    setTitleHangs: (hangs) => {
      if (hangs) {
        titleGate = Deferred.makeUnsafe<void>()
        return
      }
      if (titleGate !== null) Deferred.doneUnsafe(titleGate, Effect.void)
      titleGate = null
    },
    observerCount: (providerSessionId) => observers.get(providerSessionId)?.size ?? 0,
    endObservations: (providerSessionId) => {
      for (const queue of observers.get(providerSessionId) ?? []) Queue.endUnsafe(queue)
      observers.delete(providerSessionId)
    },
    setCheckActivity: (providerSessionId, activity) => {
      if (activity === null) checkActivity.delete(providerSessionId)
      else checkActivity.set(providerSessionId, activity)
    },
    checkCalls,
    setCheckHangs: (hangs) => {
      if (hangs) {
        checkGate = Deferred.makeUnsafe<void>()
        return
      }
      if (checkGate !== null) Deferred.doneUnsafe(checkGate, Effect.void)
      checkGate = null
    },
    maxConcurrentChecks: () => maxInFlightChecks,
    resetCheckStats: () => {
      checkCalls.length = 0
      maxInFlightChecks = 0
    },
    setSessionPruned: (providerSessionId, pruned) => {
      if (pruned) prunedSessions.add(providerSessionId)
      else prunedSessions.delete(providerSessionId)
    },
    prepared,
    released,
  }
}
