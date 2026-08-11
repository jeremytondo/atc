import type { SDKMessage, SDKUserMessage } from "@anthropic-ai/claude-agent-sdk"
import type { ClaudeQueryFn } from "../../src/agents/claudeAdapter.ts"

// A scripted stand-in for the Agent SDK's query() (the ClaudeQueryFn seam):
// consumes the streaming prompt like the real SDK, emits the message shapes
// the probes recorded (system/init only after the first user message —
// the load-bearing timing fact), and mimics the SDK's throw-after-error-
// result iterator behavior. Turn behavior keys off the input text:
//   "HANG"       — stays running until interrupt()
//   "PERMISSION" — invokes canUseTool (Bash) before finishing
//   "ASK"        — invokes canUseTool (AskUserQuestion) before finishing
//   "FAILTURN"   — ends with error_max_turns, then the iterator throws

export interface FakeClaudeQueryOptions {
  /** session_id for created sessions (resumes echo the resume id). */
  readonly sessionId?: string
  /** Session ids that resume finds; anything else fails closed. */
  readonly knownSessions?: ReadonlyArray<string>
  /** Lie about the cwd in system/init. */
  readonly wrongCwd?: boolean
  /** Report this session_id in init regardless of the resume id. */
  readonly reportSessionId?: string
  /**
   * Suppress session_state_changed events (the un-opted-in SDK shape), so
   * tests can prove activity flows from the in-process hook callbacks too.
   */
  readonly stateEvents?: boolean
}

export interface FakeClaudeQuery {
  readonly queryFn: ClaudeQueryFn
  /** Deny results canUseTool handed back, for assertions. */
  readonly denials: Array<{ toolName: string; behavior: string }>
  /** Interrupt calls observed. */
  readonly interrupts: Array<string>
  /**
   * Fire one hook event into the most recent query's registered callbacks
   * with a scripted payload (session_id filled in) — how tests replay
   * recorded background-task evidence (ATC-158) between turns.
   */
  readonly fireHook: (eventName: string, payload?: Record<string, unknown>) => Promise<void>
}

export const makeFakeClaudeQuery = (options: FakeClaudeQueryOptions = {}): FakeClaudeQuery => {
  const denials: FakeClaudeQuery["denials"] = []
  const interrupts: Array<string> = []
  let lastFire: ((eventName: string, payload?: Record<string, unknown>) => Promise<void>) | null =
    null

  const queryFn: ClaudeQueryFn = (args) => {
    const output: Array<SDKMessage> = []
    let wake: (() => void) | null = null
    let done = false
    let throwNext: string | null = null
    let hanging: string | null = null
    const resumeId = (args.options as { resume?: string }).resume
    const sessionId =
      options.reportSessionId ?? resumeId ?? options.sessionId ?? "fake-claude-session"
    const cwd = (options.wrongCwd ? "/somewhere/else" : args.options.cwd) ?? "/"

    const push = (message: Record<string, unknown>): void => {
      output.push(message as unknown as SDKMessage)
      wake?.()
    }
    const finish = (): void => {
      done = true
      wake?.()
    }
    const state = (value: string): void => {
      if (options.stateEvents === false) return
      push({
        type: "system",
        subtype: "session_state_changed",
        session_id: sessionId,
        state: value,
      })
    }
    // Deliver the in-process hook vocabulary exactly like the SDK: the
    // registered callback for the event, with the hook payload shape.
    const fireHook = async (
      eventName: string,
      payload?: Record<string, unknown>,
    ): Promise<void> => {
      const matchers = (
        args.options.hooks as
          Record<string, Array<{ hooks: Array<(input: unknown) => Promise<unknown>> }>> | undefined
      )?.[eventName]
      for (const matcher of matchers ?? []) {
        for (const callback of matcher.hooks) {
          await callback({ session_id: sessionId, hook_event_name: eventName, ...payload })
        }
      }
    }
    lastFire = fireHook
    const initOnce = (() => {
      let sent = false
      return () => {
        if (sent) return
        sent = true
        push({ type: "system", subtype: "init", session_id: sessionId, cwd })
      }
    })()

    const runTurn = async (text: string): Promise<void> => {
      // Invalid resume: exactly one error result, no init, no new session.
      if (resumeId !== undefined && !(options.knownSessions ?? []).includes(resumeId)) {
        push({
          type: "result",
          subtype: "error_during_execution",
          session_id: resumeId,
          is_error: true,
          errors: ["No conversation found with session ID"],
        })
        finish()
        return
      }
      initOnce()
      state("running")
      // Marked BEFORE the awaited hook fire: an interrupt() arriving while
      // the hook callback runs must already see the hanging turn, or it
      // records "(no hanging turn)" and never ends the query.
      const hangs = text.includes("HANG")
      if (hangs) hanging = text
      await fireHook("UserPromptSubmit")
      if (hangs) return
      if (text.includes("PERMISSION") || text.includes("ASK")) {
        const toolName = text.includes("ASK") ? "AskUserQuestion" : "Bash"
        state("requires_action")
        const result = await args.options.canUseTool?.(
          toolName,
          { command: "pwd" },
          {
            signal: new AbortController().signal,
            requestId: "fake-request-1",
            toolUseID: "fake-tool-use-1",
          },
        )
        denials.push({ toolName, behavior: (result as { behavior: string }).behavior })
        state("running")
      }
      if (text.includes("FAILTURN")) {
        push({
          type: "result",
          subtype: "error_max_turns",
          session_id: sessionId,
          is_error: true,
          errors: ["Reached maximum number of turns (1)"],
        })
        throwNext = "Claude Code returned an error result: Reached maximum number of turns (1)"
        finish()
        return
      }
      await fireHook("Stop")
      push({
        type: "result",
        subtype: "success",
        session_id: sessionId,
        is_error: false,
        result: `fake: ${text}`,
      })
      state("idle")
    }

    // Drain the prompt stream like the real SDK does.
    void (async () => {
      for await (const message of args.prompt) {
        const content = (message as SDKUserMessage).message.content
        await runTurn(typeof content === "string" ? content : JSON.stringify(content))
        if (done) return
      }
    })()

    args.options.abortController?.signal.addEventListener("abort", () => finish())

    const iterator: AsyncIterable<SDKMessage> & { interrupt: () => Promise<unknown> } = {
      interrupt: () => {
        interrupts.push(hanging ?? "(no hanging turn)")
        if (hanging !== null) {
          hanging = null
          push({
            type: "result",
            subtype: "error_during_execution",
            session_id: sessionId,
            is_error: true,
            errors: ["aborted_streaming"],
          })
          finish()
        }
        return Promise.resolve({ still_queued: [] })
      },
      [Symbol.asyncIterator]() {
        return {
          async next(): Promise<IteratorResult<SDKMessage>> {
            while (true) {
              const message = output.shift()
              if (message !== undefined) return { done: false, value: message }
              if (throwNext !== null && output.length === 0) {
                const reason = throwNext
                throwNext = null
                throw new Error(reason)
              }
              if (done) return { done: true, value: undefined }
              await new Promise<void>((resolve) => {
                wake = () => {
                  wake = null
                  resolve()
                }
              })
            }
          },
        }
      },
    }
    return iterator
  }

  return {
    queryFn,
    denials,
    interrupts,
    fireHook: (eventName, payload) => {
      if (lastFire === null) throw new Error("no query started yet")
      return lastFire(eventName, payload)
    },
  }
}
