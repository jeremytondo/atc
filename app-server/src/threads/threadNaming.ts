import { Context, Deferred, Duration, Effect, Fiber, Layer, Option } from "effect"
import { AgentRegistry } from "../agents/agentRegistry.ts"
import type { AgentActivity, AgentAdapter } from "../agents/agentAdapter.ts"
import { isBusyActivity, sanitizeTitle } from "../agents/agentAdapter.ts"
import { isAgentId } from "../api/contract.ts"
import { Events } from "../events/events.ts"
import { ThreadRepository } from "./threadRepository.ts"
import type { ThreadRecord } from "./threadRepository.ts"

// Thread auto-naming (ATC-155) and title refinement (ATC-190): one flow for
// every surface (ATC-202). Whichever surface takes a thread's first turn —
// a TUI observed by threads.ts or a native turn the ThreadRuntime drives —
// feeds the same three signals here (the first user prompt, the ledger's
// activity transitions, the end of the evidence feed) and the result is
// indistinguishable. Invariants:
//
//   - Eligibility: naming opens for a thread only on a record that is
//     unnamed AND unconfirmed — the terminal observation's record at
//     subscription start, the runtime's record as adopted for the first
//     native turn (before that turn confirms it). Retro-naming is a
//     non-goal, so a confirmed record never opens naming. Once open, the
//     per-thread state outlives the surface that opened it, and every later
//     signal on that thread — from any surface — feeds the same state. That
//     is what makes "named once" hold across surfaces (a native first turn
//     followed by a TUI turn) and across the Codex prompt discovery that
//     lags busy.
//   - The refinement is bound to the provider session it was armed on: a
//     turn end or feed end reported for another session is ignored. An
//     unconfirmed thread re-materializes (a native prompt replaces the TUI's
//     idle session), and the superseded session's observation ending must
//     not spend the live turn's refinement.
//   - Auto-naming: the first prompt forks one fire-and-forget title
//     generation through the adapter seam (verbatim prompt + cwd). A
//     creation-time or manual name always wins — checked before the
//     generation call and enforced atomically by the guarded rename — and
//     every failure leaves the fallback name, silently.
//   - Refinement: at most ONE bounded refinement per thread, from the
//     conversation the primary agent naturally produces
//     (collectTitleContext). Armed by the first busy evidence, it fires at
//     the refine delay or the busy→idle turn end, whichever comes first,
//     with one catch-up at turn end when the early collect found nothing
//     usable. The guarded rename binds the first-pass title as its expected
//     value (the seed), so a manual rename racing the refinement wins
//     atomically and the guard is self-verifying. Every failure is silent —
//     a thread never ends up worse than its first-pass name, and its name
//     visibly changes at most once after the instant one.
//   - State is in-memory only, kept until the thread is archived or
//     deleted so a late signal on a spent thread stays inert: a restart
//     forfeits a pending refinement, never a name.

export interface ThreadNamingOptions {
  /** How far into a thread's first turn the title refinement fires when
   * the turn has not ended yet (ATC-190). */
  readonly titleRefineDelay?: Duration.Input
}

export class ThreadNaming extends Context.Service<
  ThreadNaming,
  {
    /** A user prompt seen on the thread, from any surface: the first one on
     * an eligible thread names it; every later one is ignored. */
    readonly notePrompt: (record: ThreadRecord, prompt: string) => Effect.Effect<void>
    /** An activity transition from the runtime's ledger: the first busy arms
     * the refinement, the busy→idle drop is the turn's end. */
    readonly noteActivity: (
      record: ThreadRecord,
      previous: AgentActivity | undefined,
      activity: AgentActivity,
    ) => Effect.Effect<void>
    /** The evidence feed for the record's session ended (its observation
     * closed, its run was abandoned): a refinement armed on that session
     * stops waiting for a turn end that will never be reported. */
    readonly noteFeedEnded: (record: ThreadRecord) => Effect.Effect<void>
    /** Drop the thread's naming state (archive/delete): a pending
     * refinement is interrupted. */
    readonly release: (id: string) => Effect.Effect<void>
  }
>()("app-server/ThreadNaming") {}

export const layerWith = (options: ThreadNamingOptions) =>
  Layer.effect(ThreadNaming)(
    Effect.gen(function* () {
      const repository = yield* ThreadRepository
      const registry = yield* AgentRegistry
      const events = yield* Events
      // Naming fibers outlive whatever fired them (a closed terminal, a
      // finished turn) but not the server.
      const serviceScope = yield* Effect.scope
      const titleRefineDelay = options.titleRefineDelay ?? "30 seconds"

      interface NamingState {
        /** First observed user prompt (best-effort; Codex discovery can lag
         * busy and even a short turn's idle edge). */
        prompt: string | null
        /** The adopted first-pass title — the refinement's write guard. */
        seed: string | null
        /** The one refinement, once armed by the first busy evidence, and
         * the provider session it was armed on (its turn-end evidence must
         * come from that session). */
        refinement: {
          readonly session: string | undefined
          readonly fiber: Fiber.Fiber<void>
        } | null
        /** Resolved when the first prompt lands. */
        readonly promptArrived: Deferred.Deferred<void>
        /** Resolved at the first busy→idle edge, or when the feed ends. */
        readonly turnEnded: Deferred.Deferred<void>
      }
      const states = new Map<string, NamingState>()

      const adapterFor = (record: ThreadRecord): AgentAdapter | undefined =>
        isAgentId(record.agentId) ? registry.adapterFor(record.agentId) : undefined

      const isBusy = isBusyActivity

      /** Existing state wins whatever the record says; a fresh one opens
       * only on an eligible record (the header's rule). */
      const stateFor = (record: ThreadRecord): NamingState | undefined => {
        const existing = states.get(record.id)
        if (existing !== undefined) return existing
        if (record.name !== undefined || record.confirmedAt !== undefined) return undefined
        const created: NamingState = {
          prompt: null,
          seed: null,
          refinement: null,
          promptArrived: Deferred.makeUnsafe<void>(),
          turnEnded: Deferred.makeUnsafe<void>(),
        }
        states.set(record.id, created)
        return created
      }

      /**
       * The auto-naming transition (ATC-155): one title one-shot through
       * the adapter seam, adopted only while the thread is still unnamed
       * (pre-checked here, enforced atomically by the guarded rename).
       * Every failure logs and leaves the fallback name — a missing title
       * is never an error, so the effect itself cannot fail.
       */
      const generateThreadTitle = (
        record: ThreadRecord,
        prompt: string,
        naming: NamingState,
      ): Effect.Effect<void> =>
        Effect.gen(function* () {
          const adapter = adapterFor(record)
          if (adapter === undefined) return
          // A rename that landed since the prompt was captured makes the
          // generation pointless — skip the provider call, not just the
          // write (T3Code semantics).
          const current = yield* repository.get(record.id)
          if (Option.isNone(current) || current.value.name !== undefined) return
          const raw = yield* adapter.generateTitle({
            cwd: record.workingDirectory,
            prompt,
          })
          const title = sanitizeTitle(raw)
          if (title === null) return
          const renamed = yield* repository.renameIfUnchanged(record.id, title, null)
          if (Option.isNone(renamed)) return
          naming.seed = title
          yield* events.publish({ resource: "thread", id: record.id, change: "updated" })
        }).pipe(
          Effect.catch((error) =>
            Effect.logDebug("thread auto-naming failed").pipe(
              Effect.annotateLogs({ threadId: record.id, reason: error.message }),
            ),
          ),
        )

      /**
       * One refinement attempt (ATC-190): pre-check the seed guard, collect
       * context through the adapter seam, rerun the titler, and write
       * through the seed-guarded rename. "noPrompt" and "noContext" report
       * which evidence was still missing (the caller may retry once);
       * every other outcome — success, guard mismatch, unusable title —
       * spends the refinement.
       */
      const attemptRefineTitle = (
        record: ThreadRecord,
        naming: NamingState,
      ): Effect.Effect<"done" | "noPrompt" | "noContext", never, never> =>
        Effect.gen(function* () {
          const adapter = adapterFor(record)
          const providerSessionId = record.providerSessionId
          if (adapter === undefined || providerSessionId === undefined) return "done" as const
          const prompt = naming.prompt
          if (prompt === null) return "noPrompt" as const
          const current = yield* repository.get(record.id)
          if (Option.isNone(current)) return "done" as const
          // The seed check: any name the server did not seed (manual or
          // creation-time) already won. Both null means the first pass
          // adopted nothing (or is still in flight) — the refinement may
          // still name the thread, guarded exactly the same way.
          const name = current.value.name ?? null
          if (name !== naming.seed) return "done" as const
          const context = yield* adapter.collectTitleContext({
            providerSessionId,
            cwd: record.workingDirectory,
          })
          if (context === null) return "noContext" as const
          const raw = yield* adapter.generateTitle({
            cwd: record.workingDirectory,
            prompt,
            refine: { context, currentTitle: name },
          })
          const title = sanitizeTitle(raw)
          // An unchanged title is the instructed no-churn outcome: no
          // write, no publish, and the refinement is still spent.
          if (title === null || title === name) return "done" as const
          const renamed = yield* repository.renameIfUnchanged(record.id, title, name)
          if (Option.isNone(renamed)) return "done" as const
          yield* events.publish({ resource: "thread", id: record.id, change: "updated" })
          return "done" as const
        }).pipe(
          Effect.catch((error) =>
            Effect.logDebug("thread title refinement failed").pipe(
              Effect.annotateLogs({ threadId: record.id, reason: error.message }),
              Effect.as("done" as const),
            ),
          ),
        )

      /**
       * The one bounded title refinement (ATC-190): wait for the refine
       * delay or the turn's end — whichever comes first — attempt, and
       * catch up at most once when evidence was still missing. Missing
       * CONTEXT retries at turn end (it accrues over the turn) and gives
       * up silently when the turn already ended. A missing PROMPT is
       * different: Codex discovers it by polling and it can lag even a
       * short turn's idle edge, so the catch-up waits for its arrival —
       * bounded by the turn's end or one more refine delay, so the fiber
       * always ends. After the catch-up the thread is spent for good.
       */
      const refineThreadTitle = (record: ThreadRecord, naming: NamingState): Effect.Effect<void> =>
        Effect.gen(function* () {
          const trigger = yield* Effect.raceFirst(
            Effect.sleep(titleRefineDelay).pipe(Effect.as("delay" as const)),
            Deferred.await(naming.turnEnded).pipe(Effect.as("turnEnd" as const)),
          )
          const outcome = yield* attemptRefineTitle(record, naming)
          if (outcome === "done") return
          if (outcome === "noContext" && trigger === "turnEnd") return
          yield* outcome === "noContext"
            ? Deferred.await(naming.turnEnded)
            : Effect.raceFirst(
                Deferred.await(naming.promptArrived),
                trigger === "turnEnd"
                  ? Effect.sleep(titleRefineDelay)
                  : Deferred.await(naming.turnEnded),
              )
          yield* attemptRefineTitle(record, naming)
        })

      /** Only an armed refinement waits on the turn's end (ending it early
       * would pre-empt one not yet armed), and only its own session's end
       * counts (the header's binding rule). */
      const endTurn = (record: ThreadRecord): void => {
        const state = states.get(record.id)
        if (state?.refinement === null || state?.refinement === undefined) return
        if (state.refinement.session !== record.providerSessionId) return
        Deferred.doneUnsafe(state.turnEnded, Effect.void)
      }

      return {
        notePrompt: (record, prompt) =>
          Effect.gen(function* () {
            const naming = stateFor(record)
            if (naming === undefined || naming.prompt !== null) return
            naming.prompt = prompt
            yield* generateThreadTitle(record, prompt, naming).pipe(Effect.forkIn(serviceScope))
            Deferred.doneUnsafe(naming.promptArrived, Effect.void)
          }),
        noteActivity: (record, previous, activity) =>
          Effect.gen(function* () {
            if (isBusy(activity)) {
              // Armed even before the prompt is known (Codex prompt
              // discovery lags busy) — the attempt reads the state at
              // fire time. The arming record is the refinement's: it
              // carries the session the context is collected from.
              const naming = stateFor(record)
              if (naming === undefined || naming.refinement !== null) return
              const fiber = yield* refineThreadTitle(record, naming).pipe(
                Effect.forkIn(serviceScope),
              )
              naming.refinement = { session: record.providerSessionId, fiber }
              return
            }
            if (previous !== undefined && isBusy(previous) && activity === "idle") {
              endTurn(record)
            }
          }),
        noteFeedEnded: (record) => Effect.sync(() => endTurn(record)),
        release: (id) =>
          Effect.gen(function* () {
            const state = states.get(id)
            if (state === undefined) return
            states.delete(id)
            if (state.refinement !== null) yield* Fiber.interrupt(state.refinement.fiber)
          }),
      }
    }),
  )

export const layer = layerWith({})
