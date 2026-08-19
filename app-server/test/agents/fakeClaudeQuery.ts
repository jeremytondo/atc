import type { ModelInfo, SDKMessage, SDKUserMessage } from "@anthropic-ai/claude-agent-sdk"
import type { ClaudeQueryFn, ClaudeQueryHandle } from "../../src/agents/claudeAdapter.ts"

// A scripted stand-in for the Agent SDK's query() (the ClaudeQueryFn seam):
// consumes the streaming prompt like the real SDK, emits the message shapes
// the probes recorded (system/init only after the first user message —
// the load-bearing timing fact), and mimics the SDK's throw-after-error-
// result iterator behavior. Turn behavior keys off the input text:
//   "HANG"       — stays running until interrupt()
//   "PERMISSION" — emits a Bash tool_use block, then invokes canUseTool for
//                  it and AWAITS the answer (the adapter parks it): allow
//                  runs the tool (tool_result), deny fails it (is_error)
//   "ASK"        — invokes canUseTool (AskUserQuestion) with two questions
//                  and awaits the answer
//   "STREAM"     — streams the reply as stream_event frames (message_start,
//                  content_block_start/delta/stop, message_stop) before the
//                  per-block `assistant` message with the same message id
//   "TOOL"       — emits a Read tool_use block and its tool_result
//   "FAILTURN"   — ends with error_max_turns, then the iterator throws
// Every successful turn ends with a per-block `assistant` text message
// (`fake: <text>`) before its result, like the SDK's own output.

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
  /** The catalog supportedModels() answers with (default: the recorded shape). */
  readonly models?: ReadonlyArray<ModelInfo>
  /** Report this permission mode in init instead of the spawned one (the
   * CLI's silent `auto` → `default` downgrade on a model without auto support). */
  readonly reportPermissionMode?: string
  /** Leave getSettings() undeclared on the handle (an SDK that dropped it). */
  readonly withoutGetSettings?: boolean
}

/** The catalog shape recorded from the CLI (2026-08-18), trimmed. */
export const FAKE_CLAUDE_MODELS: ReadonlyArray<ModelInfo> = [
  {
    value: "default",
    resolvedModel: "claude-opus-5[1m]",
    displayName: "Default (recommended)",
    description: "Opus 5 with 1M context",
    supportsEffort: true,
    supportedEffortLevels: ["low", "medium", "high", "xhigh", "max"],
  },
  {
    value: "opus[1m]",
    resolvedModel: "claude-opus-5[1m]",
    displayName: "Opus (1M context)",
    description: "Opus 5 with 1M context",
    supportsEffort: true,
    supportedEffortLevels: ["low", "medium", "high", "xhigh", "max"],
  },
  {
    value: "sonnet",
    resolvedModel: "claude-sonnet-5",
    displayName: "Sonnet",
    description: "Sonnet 5",
    supportsEffort: true,
    supportedEffortLevels: ["low", "medium", "high", "xhigh", "max"],
  },
  {
    value: "haiku",
    resolvedModel: "claude-haiku-4-5-20251001",
    displayName: "Haiku",
    description: "Haiku 4.5",
  },
]

/** The wire id the CLI reports for a catalog value (init's `model`). */
const wireModel = (value: string | undefined): string =>
  FAKE_CLAUDE_MODELS.find((model) => model.value === value)?.resolvedModel ??
  value ??
  "claude-opus-5[1m]"

export interface FakeClaudeQuery {
  readonly queryFn: ClaudeQueryFn
  /** PermissionResults canUseTool handed back, for assertions. */
  readonly decisions: Array<{ toolName: string; result: unknown }>
  /** Interrupt calls observed. */
  readonly interrupts: Array<string>
  /** Live control requests observed (setModel / applyFlagSettings /
   * setPermissionMode), in order, with their argument. */
  readonly controls: Array<string>
  /** The query() options each spawn received, in order. */
  readonly spawns: Array<Record<string, unknown>>
  /**
   * Fire one hook event into the most recent query's registered callbacks
   * with a scripted payload (session_id filled in) — how tests replay
   * recorded background-task evidence (ATC-158) between turns.
   */
  readonly fireHook: (eventName: string, payload?: Record<string, unknown>) => Promise<void>
}

export const makeFakeClaudeQuery = (options: FakeClaudeQueryOptions = {}): FakeClaudeQuery => {
  const decisions: FakeClaudeQuery["decisions"] = []
  let nextMessage = 1
  const interrupts: Array<string> = []
  const controls: Array<string> = []
  const spawns: Array<Record<string, unknown>> = []
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
    spawns.push({ ...args.options } as unknown as Record<string, unknown>)
    // The applied settings, as the CLI would hold them: spawned, then moved
    // by the control requests.
    let model = wireModel(args.options.model)
    let permissionMode: string =
      options.reportPermissionMode ?? args.options.permissionMode ?? "default"
    let effort: string | null = args.options.effort ?? "high"

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
    /** One per-block assistant message, like the SDK emits (uuid + API message id). */
    const assistant = (messageId: string, block: Record<string, unknown>): void => {
      push({
        type: "assistant",
        uuid: crypto.randomUUID(),
        session_id: sessionId,
        parent_tool_use_id: null,
        message: { id: messageId, type: "message", role: "assistant", content: [block] },
      })
    }
    const streamEvent = (event: Record<string, unknown>): void => {
      push({
        type: "stream_event",
        uuid: crypto.randomUUID(),
        session_id: sessionId,
        parent_tool_use_id: null,
        event,
      })
    }
    const initOnce = (() => {
      let sent = false
      return () => {
        if (sent) return
        sent = true
        push({ type: "system", subtype: "init", session_id: sessionId, cwd, model, permissionMode })
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
      const messageId = `msg_fake_${nextMessage++}`
      if (text.includes("STREAM")) {
        const reply = `fake: ${text}`
        streamEvent({ type: "message_start", message: { id: messageId, role: "assistant" } })
        streamEvent({
          type: "content_block_start",
          index: 0,
          content_block: { type: "text", text: "" },
        })
        for (const delta of ["fake: ", text]) {
          streamEvent({
            type: "content_block_delta",
            index: 0,
            delta: { type: "text_delta", text: delta },
          })
        }
        streamEvent({ type: "content_block_stop", index: 0 })
        streamEvent({ type: "message_stop" })
        assistant(messageId, { type: "text", text: reply })
      }
      if (text.includes("TOOL")) {
        assistant(messageId, {
          type: "tool_use",
          id: "toolu_fake_read",
          name: "Read",
          input: { file_path: "/work/notes.md" },
        })
        push({
          type: "user",
          uuid: crypto.randomUUID(),
          session_id: sessionId,
          parent_tool_use_id: null,
          message: {
            role: "user",
            content: [{ type: "tool_result", tool_use_id: "toolu_fake_read", content: "hello" }],
          },
          tool_use_result: { file: { filePath: "/work/notes.md", content: "hello" } },
        })
      }
      if (text.includes("PERMISSION") || text.includes("ASK")) {
        const ask = text.includes("ASK")
        const toolName = ask ? "AskUserQuestion" : "Bash"
        const input = ask
          ? {
              questions: [
                {
                  question: "Which color?",
                  header: "Color",
                  options: [
                    { label: "red", description: "warm" },
                    { label: "blue", description: "cool" },
                  ],
                  multiSelect: false,
                },
                { question: "Any notes?", header: "Notes", options: [], multiSelect: false },
              ],
            }
          : { command: "pwd" }
        if (!ask)
          assistant(messageId, { type: "tool_use", id: "fake-tool-use-1", name: toolName, input })
        state("requires_action")
        const result = await args.options.canUseTool?.(toolName, input, {
          signal: new AbortController().signal,
          requestId: "fake-request-1",
          toolUseID: "fake-tool-use-1",
          ...(ask
            ? {}
            : { title: "Claude wants to run pwd", decisionReason: "outside the allow list" }),
        })
        decisions.push({ toolName, result })
        state("running")
        const denied = (result as { behavior?: string } | null)?.behavior !== "allow"
        if (!ask) {
          push({
            type: "user",
            uuid: crypto.randomUUID(),
            session_id: sessionId,
            parent_tool_use_id: null,
            message: {
              role: "user",
              content: [
                {
                  type: "tool_result",
                  tool_use_id: "fake-tool-use-1",
                  content: denied ? "permission denied" : "/work",
                  is_error: denied,
                },
              ],
            },
          })
        }
        if ((result as { interrupt?: boolean } | null)?.interrupt === true) {
          push({
            type: "result",
            subtype: "error_during_execution",
            session_id: sessionId,
            is_error: true,
            errors: ["aborted_streaming"],
          })
          finish()
          return
        }
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
      if (!text.includes("STREAM")) assistant(messageId, { type: "text", text: `fake: ${text}` })
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

    const getSettings = () => Promise.resolve({ applied: { model, effort } })
    const iterator: ClaudeQueryHandle & { getSettings?: () => Promise<unknown> } = {
      setModel: (next) => {
        controls.push(`setModel:${next ?? ""}`)
        model = wireModel(next)
        return Promise.resolve()
      },
      setPermissionMode: (next) => {
        controls.push(`setPermissionMode:${next}`)
        permissionMode = next
        return Promise.resolve()
      },
      applyFlagSettings: (settings) => {
        controls.push(`applyFlagSettings:${settings.effortLevel ?? "null"}`)
        effort = settings.effortLevel ?? null
        return Promise.resolve()
      },
      supportedModels: () => Promise.resolve(options.models ?? FAKE_CLAUDE_MODELS),
      ...(options.withoutGetSettings === true ? {} : { getSettings }),
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
    decisions,
    interrupts,
    controls,
    spawns,
    fireHook: (eventName, payload) => {
      if (lastFire === null) throw new Error("no query started yet")
      return lastFire(eventName, payload)
    },
  }
}
