import {
  BoxRenderable,
  createCliRenderer,
  InputRenderable,
  InputRenderableEvents,
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

const mainInput = (
  key: KeyEvent,
  snapshot: AppServer.Snapshot | undefined,
  state: ManagerState,
):
  | MainResult
  | { readonly type: "state"; readonly state: ManagerState }
  | { readonly type: "refresh"; readonly state: ManagerState } => {
  if (key.ctrl && key.name === "c") {
    return { type: "quit", selectedThreadId: state.selectedThreadId }
  }
  if (key.ctrl && key.name === "n") return { type: "new", state }
  if (key.name === "q") return { type: "quit", selectedThreadId: state.selectedThreadId }
  if (key.name === "up" || key.name === "k") {
    return {
      type: "state",
      state: {
        ...state,
        selectedThreadId: View.moveSelection(snapshot, state.selectedThreadId, -1),
        status: undefined,
      },
    }
  }
  if (key.name === "down" || key.name === "j") {
    return {
      type: "state",
      state: {
        ...state,
        selectedThreadId: View.moveSelection(snapshot, state.selectedThreadId, 1),
        status: undefined,
      },
    }
  }
  if (key.name === "r") {
    return { type: "refresh", state: { ...state, status: "Refreshing…" } }
  }
  if (key.name === "return" || key.name === "enter") {
    const threadId = View.normalizeSelection(snapshot, state.selectedThreadId)
    return threadId === undefined
      ? {
          type: "state",
          state: { ...state, status: "There is no active Thread to attach." },
        }
      : { type: "attach", threadId }
  }
  return { type: "state", state }
}

const runMainScreen = (
  renderer: CliRenderer,
  options: ManagerOptions,
  initial: ManagerState,
): Effect.Effect<MainResult, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const keys = yield* Queue.unbounded<KeyEvent>()
      const screen = new BoxRenderable(renderer, {
        id: "manager",
        width: "100%",
        height: "100%",
        padding: 1,
      })
      const content = new TextRenderable(renderer, {
        id: "manager-content",
        width: "100%",
        height: "100%",
        content: "",
      })
      screen.add(content)
      yield* acquireScreen(renderer, screen)

      const onKey = (key: KeyEvent) => {
        Queue.offerUnsafe(keys, key)
      }
      renderer.keyInput.on("keypress", onKey)
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => {
          renderer.keyInput.off("keypress", onKey)
        }),
      )

      const render = (state: ManagerState) =>
        Effect.gen(function* () {
          const snapshot = yield* Ref.get(options.snapshotRef)
          const reachability = yield* Ref.get(options.reachabilityRef)
          const backgroundStatus = yield* Ref.get(options.backgroundStatusRef)
          content.content = View.render({
            endpoint: options.endpoint,
            reachability,
            snapshot,
            state: {
              selectedThreadId: View.normalizeSelection(snapshot, state.selectedThreadId),
              status: state.status ?? backgroundStatus,
            },
            columns: renderer.terminalWidth - 2,
            rows: renderer.terminalHeight - 2,
          })
        })

      const loop = (state: ManagerState): Effect.Effect<MainResult, unknown> =>
        render(state).pipe(
          Effect.andThen(
            Effect.race(
              Queue.take(keys).pipe(Effect.map((key) => ({ type: "key" as const, key }))),
              Queue.take(options.uiUpdates).pipe(Effect.as({ type: "update" as const })),
            ),
          ),
          Effect.flatMap((event) => {
            if (event.type === "update") {
              return Ref.get(options.snapshotRef).pipe(
                Effect.flatMap((snapshot) =>
                  loop({
                    selectedThreadId: View.normalizeSelection(snapshot, state.selectedThreadId),
                  }),
                ),
              )
            }

            return Ref.get(options.snapshotRef).pipe(
              Effect.flatMap((snapshot) => {
                const action = mainInput(event.key, snapshot, state)
                if (action.type === "state") return loop(action.state)
                if (action.type === "refresh") {
                  return Queue.offer(options.refreshRequests, void 0).pipe(
                    Effect.andThen(loop(action.state)),
                  )
                }
                return Effect.succeed(action)
              }),
            )
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

const inputPrompt = (
  renderer: CliRenderer,
  project: AppServer.Project,
  agent: AppServer.Agent,
): Effect.Effect<PromptResult<string>> =>
  Effect.scoped(
    Effect.gen(function* () {
      const results = yield* Queue.unbounded<PromptResult<string>>()
      const screen = new BoxRenderable(renderer, {
        id: "prompt-name",
        title: "New Thread",
        border: true,
        borderStyle: "rounded",
        width: "100%",
        height: "100%",
        padding: 1,
        flexDirection: "column",
        gap: 1,
      })
      const context = new TextRenderable(renderer, {
        id: "prompt-context",
        content: `Project: ${project.name}\nAgent: ${agent.id}\n\nThread name`,
      })
      const input = new InputRenderable(renderer, {
        id: "prompt-name-input",
        width: "100%",
        maxLength: 120,
        placeholder: "Name this session",
        backgroundColor: "#1f2937",
        focusedBackgroundColor: "#374151",
        textColor: "#f9fafb",
        cursorColor: "#60a5fa",
      })
      const status = new TextRenderable(renderer, {
        id: "prompt-name-status",
        content: "",
        fg: "#f87171",
        height: 1,
      })
      const help = new TextRenderable(renderer, {
        id: "prompt-name-help",
        content: "Enter create and attach · Esc cancel · Ctrl-C quit",
        fg: "#888888",
      })
      screen.add(context)
      screen.add(input)
      screen.add(status)
      screen.add(help)
      yield* acquireScreen(renderer, screen)

      const onEnter = (value: string) => {
        const name = value.trim()
        if (name === "") {
          status.content = "A Thread name is required."
          input.focus()
          return
        }
        Queue.offerUnsafe(results, { type: "value", value: name })
      }
      const onKey = (key: KeyEvent) => {
        if (key.ctrl && key.name === "c") {
          Queue.offerUnsafe(results, { type: "quit" })
          return
        }
        if (key.name === "escape") Queue.offerUnsafe(results, { type: "cancel" })
      }
      input.on(InputRenderableEvents.ENTER, onEnter)
      renderer.keyInput.on("keypress", onKey)
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => {
          input.off(InputRenderableEvents.ENTER, onEnter)
          renderer.keyInput.off("keypress", onKey)
        }),
      )
      input.focus()

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

    const name = yield* inputPrompt(renderer, project.value, agent.value)
    if (name.type === "quit") return name
    if (name.type === "cancel") {
      return { type: "cancel", status: "Thread creation cancelled." } as const
    }

    return {
      type: "create",
      input: {
        projectId: project.value.id,
        agentId: agent.value.id,
        name: name.value,
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
