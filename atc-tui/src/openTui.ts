import {
  BoxRenderable,
  createCliRenderer,
  SelectRenderable,
  SelectRenderableEvents,
  TextRenderable,
  type CliRenderer,
  type KeyEvent,
} from "@opentui/core"
import { Effect, Queue, Ref } from "effect"
import * as AppServer from "./appServer.ts"
import * as View from "./view.ts"

// OpenTUI owns only the interactive manager surface. Callbacks publish typed
// events into Effect queues; API work remains in the application coordinator.
// Each manager run owns a renderer scope that is destroyed before zmx inherits
// the real TTY.

export interface ManagerState {
  readonly selectedThreadId?: string | undefined
  readonly status?: string | undefined
}

export type ManagerResult =
  | { readonly type: "quit"; readonly selectedThreadId?: string | undefined }
  | { readonly type: "attach"; readonly threadId: string }
  | {
      readonly type: "create"
      readonly input: AppServer.CreateThreadInput
      readonly selectedThreadId?: string | undefined
    }

export interface ManagerOptions {
  readonly endpoint: URL
  readonly snapshotRef: Ref.Ref<AppServer.Snapshot | undefined>
  readonly reachabilityRef: Ref.Ref<View.Reachability>
  readonly backgroundStatusRef: Ref.Ref<string | undefined>
  readonly uiUpdates: Queue.Dequeue<void>
  readonly refreshRequests: Queue.Enqueue<void>
  readonly initial: ManagerState
}

type MainResult =
  | ManagerResult
  | {
      readonly type: "new"
      readonly state: ManagerState
    }

type PromptResult<Value> =
  | { readonly type: "value"; readonly value: Value }
  | { readonly type: "cancel" }
  | { readonly type: "quit" }

interface PromptItem<Value> {
  readonly name: string
  readonly description: string
  readonly value: Value
}

const describeError = (error: unknown): string =>
  error instanceof Error && error.message !== "" ? error.message : String(error)

const acquireRenderer = Effect.acquireRelease(
  Effect.tryPromise({
    try: () =>
      createCliRenderer({
        exitOnCtrlC: false,
        exitSignals: [],
        useMouse: false,
        autoFocus: false,
        openConsoleOnError: false,
      }),
    catch: (error) => new Error(`OpenTUI renderer failed: ${describeError(error)}`),
  }),
  (renderer) =>
    Effect.try({
      try: () => renderer.destroy(),
      catch: () => undefined,
    }).pipe(Effect.ignore),
)

const acquireScreen = (renderer: CliRenderer, screen: BoxRenderable) =>
  Effect.acquireRelease(
    Effect.sync(() => {
      renderer.root.add(screen)
      return screen
    }),
    (owned) =>
      Effect.try({
        try: () => {
          if (owned.parent !== null) renderer.root.remove(owned)
          owned.destroyRecursively()
        },
        catch: () => undefined,
      }).pipe(Effect.ignore),
  )

type MainEvent =
  | { readonly type: "selected"; readonly threadId: string }
  | { readonly type: "attach"; readonly threadId: string }
  | { readonly type: "new" }
  | { readonly type: "refresh" }
  | { readonly type: "quit" }

const runMainScreen = (
  renderer: CliRenderer,
  options: ManagerOptions,
  initial: ManagerState,
): Effect.Effect<MainResult, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const events = yield* Queue.unbounded<MainEvent>()
      const threadIdsByIndex: Array<string> = []
      const screen = new BoxRenderable(renderer, {
        id: "manager",
        width: "100%",
        height: "100%",
        padding: 1,
        gap: 1,
        flexDirection: "column",
        backgroundColor: "#0b1020",
      })
      const header = new TextRenderable(renderer, {
        id: "manager-header",
        height: 2,
        content: "",
        fg: "#e2e8f0",
      })
      const list = new BoxRenderable(renderer, {
        id: "manager-list",
        title: " Threads ",
        titleColor: "#93c5fd",
        border: true,
        borderStyle: "rounded",
        borderColor: "#334155",
        flexGrow: 1,
        padding: 1,
        backgroundColor: "#111827",
      })
      const threads = new SelectRenderable(renderer, {
        id: "manager-threads",
        width: "100%",
        height: "100%",
        options: [],
        wrapSelection: false,
        showDescription: true,
        showScrollIndicator: true,
        backgroundColor: "#111827",
        focusedBackgroundColor: "#111827",
        selectedBackgroundColor: "#1d4ed8",
        selectedTextColor: "#ffffff",
        selectedDescriptionColor: "#dbeafe",
        descriptionColor: "#94a3b8",
      })
      const empty = new TextRenderable(renderer, {
        id: "manager-empty",
        width: "100%",
        height: "100%",
        content: "",
        fg: "#94a3b8",
      })
      const status = new TextRenderable(renderer, {
        id: "manager-status",
        height: 1,
        content: "",
        fg: "#fbbf24",
      })
      const help = new TextRenderable(renderer, {
        id: "manager-help",
        height: 2,
        content:
          "↑/↓ or j/k navigate  ·  Enter attach  ·  Ctrl-N new  ·  r refresh  ·  q quit\n" +
          "Inside zmx, Ctrl-\\ returns here",
        fg: "#64748b",
      })
      list.add(threads)
      list.add(empty)
      screen.add(header)
      screen.add(list)
      screen.add(status)
      screen.add(help)
      yield* acquireScreen(renderer, screen)

      const onKey = (key: KeyEvent) => {
        if (key.ctrl && key.name === "c") {
          Queue.offerUnsafe(events, { type: "quit" })
          return
        }
        if (key.ctrl && key.name === "n") {
          Queue.offerUnsafe(events, { type: "new" })
          return
        }
        if (key.name === "q") {
          Queue.offerUnsafe(events, { type: "quit" })
          return
        }
        if (key.name === "r") Queue.offerUnsafe(events, { type: "refresh" })
      }
      const onSelectionChanged = (index: number) => {
        const threadId = threadIdsByIndex[index]
        if (threadId !== undefined) Queue.offerUnsafe(events, { type: "selected", threadId })
      }
      const onItemSelected = (index: number) => {
        const threadId = threadIdsByIndex[index]
        if (threadId !== undefined) Queue.offerUnsafe(events, { type: "attach", threadId })
      }
      renderer.keyInput.on("keypress", onKey)
      threads.on(SelectRenderableEvents.SELECTION_CHANGED, onSelectionChanged)
      threads.on(SelectRenderableEvents.ITEM_SELECTED, onItemSelected)
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => {
          renderer.keyInput.off("keypress", onKey)
          threads.off(SelectRenderableEvents.SELECTION_CHANGED, onSelectionChanged)
          threads.off(SelectRenderableEvents.ITEM_SELECTED, onItemSelected)
        }),
      )
      threads.focus()

      const render = (state: ManagerState) =>
        Effect.gen(function* () {
          const snapshot = yield* Ref.get(options.snapshotRef)
          const reachability = yield* Ref.get(options.reachabilityRef)
          const backgroundStatus = yield* Ref.get(options.backgroundStatusRef)
          const items = View.threadOptions(snapshot)
          const selectedThreadId = View.normalizeSelection(snapshot, state.selectedThreadId)
          const selectedIndex = Math.max(
            0,
            items.findIndex((item) => item.threadId === selectedThreadId),
          )

          threadIdsByIndex.splice(0, threadIdsByIndex.length, ...items.map((item) => item.threadId))
          threads.options = items.map((item) => ({
            name: item.name,
            description: item.description,
          }))
          if (items.length > 0 && threads.getSelectedIndex() !== selectedIndex) {
            threads.setSelectedIndex(selectedIndex)
          }
          threads.visible = items.length > 0
          empty.visible = items.length === 0
          empty.content =
            snapshot === undefined
              ? "Waiting for the App Server…"
              : snapshot.projects.length === 0
                ? "No Projects yet. Create one before starting a Thread."
                : "No active Threads. Press Ctrl-N to create one."
          list.title = ` Threads (${items.length}) `
          const fetched =
            snapshot === undefined ? "" : `  ·  synced ${snapshot.fetchedAt.toLocaleTimeString()}`
          header.content =
            `ATC\n${options.endpoint.origin}  ·  ${View.connectionLabel(reachability)}` + fetched
          status.content = state.status ?? backgroundStatus ?? ""
        })

      const loop = (state: ManagerState): Effect.Effect<MainResult, unknown> =>
        render(state).pipe(
          Effect.andThen(
            Effect.race(
              Queue.take(events),
              Queue.take(options.uiUpdates).pipe(Effect.as({ type: "update" as const })),
            ),
          ),
          Effect.flatMap((event) => {
            if (event.type === "update") {
              return Ref.get(options.snapshotRef).pipe(
                Effect.flatMap((snapshot) =>
                  loop({
                    ...state,
                    selectedThreadId: View.normalizeSelection(snapshot, state.selectedThreadId),
                  }),
                ),
              )
            }
            if (event.type === "selected") {
              return loop({ selectedThreadId: event.threadId })
            }
            if (event.type === "refresh") {
              return Queue.offer(options.refreshRequests, void 0).pipe(
                Effect.andThen(loop({ ...state, status: "Refreshing…" })),
              )
            }
            if (event.type === "new") return Effect.succeed({ type: "new", state })
            if (event.type === "quit") {
              return Effect.succeed({ type: "quit", selectedThreadId: state.selectedThreadId })
            }
            return Effect.succeed(event)
          }),
        )

      return yield* loop(initial)
    }),
  )

const selectPrompt = <Value>(
  renderer: CliRenderer,
  title: string,
  items: ReadonlyArray<PromptItem<Value>>,
  selectedIndex: number,
): Effect.Effect<PromptResult<Value>> =>
  Effect.scoped(
    Effect.gen(function* () {
      const results = yield* Queue.unbounded<PromptResult<Value>>()
      const screen = new BoxRenderable(renderer, {
        id: "prompt-select",
        title,
        border: true,
        borderStyle: "rounded",
        width: "100%",
        height: "100%",
        padding: 1,
        flexDirection: "column",
        gap: 1,
      })
      const select = new SelectRenderable(renderer, {
        id: "prompt-options",
        width: "100%",
        height: Math.max(3, Math.min(items.length * 2, renderer.terminalHeight - 8)),
        options: items.map((item) => ({
          name: item.name,
          description: item.description,
        })),
        selectedIndex: Math.max(0, Math.min(items.length - 1, selectedIndex)),
        wrapSelection: true,
        showDescription: true,
      })
      const help = new TextRenderable(renderer, {
        id: "prompt-help",
        content: "↑/↓ select · Enter continue · Esc cancel · Ctrl-C quit",
        fg: "#888888",
      })
      screen.add(select)
      screen.add(help)
      yield* acquireScreen(renderer, screen)

      const onSelected = (index: number) => {
        const item = items[index]
        if (item !== undefined) Queue.offerUnsafe(results, { type: "value", value: item.value })
      }
      const onKey = (key: KeyEvent) => {
        if (key.ctrl && key.name === "c") {
          Queue.offerUnsafe(results, { type: "quit" })
          return
        }
        if (key.name === "escape") Queue.offerUnsafe(results, { type: "cancel" })
      }
      select.on(SelectRenderableEvents.ITEM_SELECTED, onSelected)
      renderer.keyInput.on("keypress", onKey)
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => {
          select.off(SelectRenderableEvents.ITEM_SELECTED, onSelected)
          renderer.keyInput.off("keypress", onKey)
        }),
      )
      select.focus()

      return yield* Queue.take(results)
    }),
  )

type WizardResult =
  | { readonly type: "create"; readonly input: AppServer.CreateThreadInput }
  | { readonly type: "cancel"; readonly status: string }
  | { readonly type: "quit" }

const runCreateWizard = (
  renderer: CliRenderer,
  snapshot: AppServer.Snapshot,
  preferredProjectId: string | undefined,
): Effect.Effect<WizardResult> =>
  Effect.gen(function* () {
    if (snapshot.projects.length === 0) {
      return { type: "cancel", status: "Create a Project before creating a Thread." } as const
    }
    const agents = snapshot.agents.filter((agent) => agent.available)
    if (agents.length === 0) {
      return {
        type: "cancel",
        status: "No installed agent is available; check the App Server agent diagnostics.",
      } as const
    }

    const preferredProjectIndex = Math.max(
      0,
      snapshot.projects.findIndex((project) => project.id === preferredProjectId),
    )
    const project = yield* selectPrompt(
      renderer,
      "New Thread · Project",
      snapshot.projects.map((item) => ({
        name: item.name,
        description: item.defaultWorkingDirectory,
        value: item,
      })),
      preferredProjectIndex,
    )
    if (project.type === "quit") return project
    if (project.type === "cancel") {
      return { type: "cancel", status: "Thread creation cancelled." } as const
    }

    const preferredAgentIndex = Math.max(
      0,
      agents.findIndex((agent) => agent.id === "codex"),
    )
    const agent = yield* selectPrompt(
      renderer,
      "New Thread · Agent",
      agents.map((item) => ({
        name: item.id,
        description: `${item.detectedVersion ?? "installed"} · default ${item.defaults.model}`,
        value: item,
      })),
      preferredAgentIndex,
    )
    if (agent.type === "quit") return agent
    if (agent.type === "cancel") {
      return { type: "cancel", status: "Thread creation cancelled." } as const
    }

    return {
      type: "create",
      input: {
        projectId: project.value.id,
        agentId: agent.value.id,
      },
    }
  })

export const runWithRenderer = (
  renderer: CliRenderer,
  options: ManagerOptions,
): Effect.Effect<ManagerResult, unknown> => {
  const loop = (state: ManagerState): Effect.Effect<ManagerResult, unknown> =>
    runMainScreen(renderer, options, state).pipe(
      Effect.flatMap((result) => {
        if (result.type !== "new") return Effect.succeed(result)

        return Ref.get(options.snapshotRef).pipe(
          Effect.flatMap((snapshot) => {
            if (snapshot === undefined) {
              return loop({ ...result.state, status: "Wait for the App Server before creating." })
            }
            const preferredProjectId = View.projectIdForSelection(
              snapshot,
              result.state.selectedThreadId,
            )
            return runCreateWizard(renderer, snapshot, preferredProjectId).pipe(
              Effect.flatMap((created) => {
                if (created.type === "quit") {
                  return Effect.succeed({
                    type: "quit",
                    selectedThreadId: result.state.selectedThreadId,
                  } as const)
                }
                if (created.type === "cancel") {
                  return loop({ ...result.state, status: created.status })
                }
                return Effect.succeed({
                  ...created,
                  selectedThreadId: result.state.selectedThreadId,
                })
              }),
            )
          }),
        )
      }),
    )

  return loop(options.initial)
}

export const run = (options: ManagerOptions): Effect.Effect<ManagerResult, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const renderer = yield* acquireRenderer
      return yield* runWithRenderer(renderer, options)
    }),
  )
