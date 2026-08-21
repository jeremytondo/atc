import {
  createCliRenderer,
  InputRenderableEvents,
  SelectRenderableEvents,
  type CliRenderer,
  type KeyEvent,
} from "@opentui/core"
import { Effect, Queue, Ref } from "effect"
import * as AppServer from "./appServer.ts"
import * as OpenTuiApp from "./openTuiApp.ts"
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
      readonly type: "createThread"
      readonly input: AppServer.CreateThreadInput
      readonly selectedThreadId?: string | undefined
    }
  | {
      readonly type: "createProject"
      readonly input: AppServer.CreateProjectInput
      readonly selectedThreadId?: string | undefined
    }
  | { readonly type: "archiveThread"; readonly threadId: string }

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
  | { readonly type: "quit"; readonly selectedThreadId?: string | undefined }
  | { readonly type: "attach"; readonly threadId: string }
  | { readonly type: "newThread"; readonly state: ManagerState }
  | { readonly type: "newProject"; readonly state: ManagerState }
  | { readonly type: "archive"; readonly threadId: string; readonly state: ManagerState }

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

type MainEvent =
  | { readonly type: "attach"; readonly threadId: string }
  | { readonly type: "newThread"; readonly selectedThreadId?: string | undefined }
  | { readonly type: "newProject"; readonly selectedThreadId?: string | undefined }
  | { readonly type: "archive"; readonly threadId: string }
  | { readonly type: "refresh"; readonly selectedThreadId?: string | undefined }
  | { readonly type: "quit"; readonly selectedThreadId?: string | undefined }

const runMainScreen = (
  shell: OpenTuiApp.AppShell,
  options: ManagerOptions,
  initial: ManagerState,
): Effect.Effect<MainResult, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const renderer = shell.renderer
      const events = yield* Queue.unbounded<MainEvent>()
      const threadIdsByIndex: Array<string> = []
      const view = OpenTuiApp.makeView(shell, "threads-view")
      const threads = OpenTuiApp.makeSelect(shell, {
        id: "threads",
        items: [],
      })
      const empty = OpenTuiApp.makeMessage(shell, "threads-empty")
      view.add(threads)
      view.add(empty)
      yield* OpenTuiApp.mountView(shell, view)

      const selectedThreadId = () => threadIdsByIndex[threads.getSelectedIndex()]
      const onKey = (key: KeyEvent) => {
        if (key.ctrl && key.name === "c") {
          Queue.offerUnsafe(events, { type: "quit", selectedThreadId: selectedThreadId() })
          return
        }
        if (key.ctrl && key.name === "n") {
          Queue.offerUnsafe(events, { type: "newThread", selectedThreadId: selectedThreadId() })
          return
        }
        if (key.ctrl && key.name === "p") {
          Queue.offerUnsafe(events, { type: "newProject", selectedThreadId: selectedThreadId() })
          return
        }
        if (key.name === "a") {
          const threadId = selectedThreadId()
          if (threadId !== undefined) Queue.offerUnsafe(events, { type: "archive", threadId })
          return
        }
        if (key.name === "q") {
          Queue.offerUnsafe(events, { type: "quit", selectedThreadId: selectedThreadId() })
          return
        }
        if (key.name === "r") {
          Queue.offerUnsafe(events, { type: "refresh", selectedThreadId: selectedThreadId() })
        }
      }
      const onItemSelected = (index: number) => {
        const threadId = threadIdsByIndex[index]
        if (threadId !== undefined) Queue.offerUnsafe(events, { type: "attach", threadId })
      }
      renderer.keyInput.on("keypress", onKey)
      threads.on(SelectRenderableEvents.ITEM_SELECTED, onItemSelected)
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => {
          renderer.keyInput.off("keypress", onKey)
          threads.off(SelectRenderableEvents.ITEM_SELECTED, onItemSelected)
        }),
      )
      threads.focus()

      const render = (state: ManagerState): Effect.Effect<void> =>
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
          if (items.length > 0 && renderer.currentFocusedRenderable !== threads) {
            threads.focus()
          }
          empty.content =
            snapshot === undefined
              ? "Waiting for the App Server…"
              : snapshot.projects.length === 0
                ? "No Projects yet. Press Ctrl-P to create one."
                : "No active Threads. Press Ctrl-N to create one."
          const fetched =
            snapshot === undefined ? "" : `  ·  synced ${snapshot.fetchedAt.toLocaleTimeString()}`
          OpenTuiApp.update(shell, {
            subtitle:
              options.endpoint.origin + `  ·  ${View.connectionLabel(reachability)}` + fetched,
            title: `Threads (${items.length})`,
            status: state.status ?? backgroundStatus,
            help:
              "↑/↓ or j/k navigate  ·  Enter attach  ·  Ctrl-N thread  ·  Ctrl-P project\n" +
              "a archive  ·  r refresh  ·  q quit  ·  zmx Ctrl-\\ returns here",
          })
        })

      const loop = (state: ManagerState): Effect.Effect<MainResult, unknown> =>
        render(state).pipe(
          Effect.andThen(
            Effect.race(
              Queue.take(events),
              Queue.take(options.uiUpdates).pipe(Effect.as({ type: "update" as const })),
            ),
          ),
          Effect.flatMap((event): Effect.Effect<MainResult, unknown> => {
            if (event.type === "update") {
              const currentThreadId = selectedThreadId()
              return Ref.get(options.snapshotRef).pipe(
                Effect.flatMap((snapshot) =>
                  loop({
                    selectedThreadId: View.normalizeSelection(snapshot, currentThreadId),
                  }),
                ),
              )
            }
            if (event.type === "refresh") {
              return Queue.offer(options.refreshRequests, void 0).pipe(
                Effect.andThen(
                  loop({ selectedThreadId: event.selectedThreadId, status: "Refreshing…" }),
                ),
              )
            }
            if (event.type === "newThread" || event.type === "newProject") {
              return Effect.succeed({
                type: event.type,
                state: { selectedThreadId: event.selectedThreadId },
              } as const)
            }
            if (event.type === "archive") {
              return Effect.succeed({
                type: "archive",
                threadId: event.threadId,
                state: { selectedThreadId: event.threadId },
              } as const)
            }
            if (event.type === "quit") {
              return Effect.succeed({
                type: "quit",
                selectedThreadId: event.selectedThreadId,
              } as const)
            }
            return Effect.succeed({ type: "attach", threadId: event.threadId } as const)
          }),
        )

      return yield* loop(initial)
    }),
  )

const selectPrompt = <Value>(
  shell: OpenTuiApp.AppShell,
  title: string,
  items: ReadonlyArray<PromptItem<Value>>,
  selectedIndex: number,
): Effect.Effect<PromptResult<Value>> =>
  Effect.scoped(
    Effect.gen(function* () {
      const renderer = shell.renderer
      const results = yield* Queue.unbounded<PromptResult<Value>>()
      const view = OpenTuiApp.makeView(shell, "prompt-view")
      const select = OpenTuiApp.makeSelect(shell, {
        id: "prompt-options",
        items,
        selectedIndex: Math.max(0, Math.min(items.length - 1, selectedIndex)),
        wrapSelection: true,
      })
      view.add(select)
      yield* OpenTuiApp.mountView(shell, view)
      OpenTuiApp.update(shell, {
        title,
        status: "",
        help: "↑/↓ or j/k select  ·  Enter continue  ·  Esc cancel  ·  Ctrl-C quit",
      })

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

interface TextPromptOptions {
  readonly title: string
  readonly label: string
  readonly placeholder: string
  readonly validate: (value: string) => string | undefined
}

const textPrompt = (
  shell: OpenTuiApp.AppShell,
  options: TextPromptOptions,
): Effect.Effect<PromptResult<string>> =>
  Effect.scoped(
    Effect.gen(function* () {
      const renderer = shell.renderer
      const results = yield* Queue.unbounded<PromptResult<string>>()
      const view = OpenTuiApp.makeFormView(shell, "text-prompt-view")
      const label = OpenTuiApp.makePromptLabel(shell, "text-prompt-label", options.label)
      const input = OpenTuiApp.makeInput(shell, {
        id: "text-prompt-input",
        placeholder: options.placeholder,
      })
      view.add(label)
      view.add(input)
      yield* OpenTuiApp.mountView(shell, view)
      OpenTuiApp.update(shell, {
        title: options.title,
        status: "",
        help: "Enter continue  ·  Esc cancel  ·  Ctrl-C quit",
      })

      const onEnter = (value: string) => {
        const normalized = value.trim()
        const problem = options.validate(normalized)
        if (problem !== undefined) {
          shell.status.content = problem
          return
        }
        Queue.offerUnsafe(results, { type: "value", value: normalized })
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

type ThreadWizardResult =
  | { readonly type: "create"; readonly input: AppServer.CreateThreadInput }
  | { readonly type: "cancel"; readonly status: string }
  | { readonly type: "quit" }

const runCreateThreadWizard = (
  shell: OpenTuiApp.AppShell,
  snapshot: AppServer.Snapshot,
  preferredProjectId: string | undefined,
): Effect.Effect<ThreadWizardResult> =>
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
      shell,
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
      shell,
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

type ProjectWizardResult =
  | { readonly type: "create"; readonly input: AppServer.CreateProjectInput }
  | { readonly type: "cancel"; readonly status: string }
  | { readonly type: "quit" }

const runCreateProjectWizard = (shell: OpenTuiApp.AppShell): Effect.Effect<ProjectWizardResult> =>
  Effect.gen(function* () {
    const name = yield* textPrompt(shell, {
      title: "New Project · Name",
      label: "Project name",
      placeholder: "e.g. ATC",
      validate: (value) => (value === "" ? "Project name is required." : undefined),
    })
    if (name.type === "quit") return name
    if (name.type === "cancel") {
      return { type: "cancel", status: "Project creation cancelled." } as const
    }

    const directory = yield* textPrompt(shell, {
      title: "New Project · Directory",
      label: "Absolute directory on the App Server host",
      placeholder: "/path/to/project",
      validate: (value) =>
        value.startsWith("/") ? undefined : "Enter an absolute path beginning with /.",
    })
    if (directory.type === "quit") return directory
    if (directory.type === "cancel") {
      return { type: "cancel", status: "Project creation cancelled." } as const
    }

    return {
      type: "create",
      input: {
        name: name.value,
        defaultWorkingDirectory: directory.value,
      },
    }
  })

export const runWithRenderer = (
  renderer: CliRenderer,
  options: ManagerOptions,
): Effect.Effect<ManagerResult, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const shell = OpenTuiApp.make(renderer)
      yield* OpenTuiApp.mount(shell)

      const loop = (state: ManagerState): Effect.Effect<ManagerResult, unknown> =>
        runMainScreen(shell, options, state).pipe(
          Effect.flatMap((result) => {
            if (result.type === "quit" || result.type === "attach") {
              return Effect.succeed(result)
            }
            if (result.type === "newProject") {
              return runCreateProjectWizard(shell).pipe(
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
                    type: "createProject",
                    input: created.input,
                    selectedThreadId: result.state.selectedThreadId,
                  } as const)
                }),
              )
            }
            if (result.type === "archive") {
              return Ref.get(options.snapshotRef).pipe(
                Effect.flatMap((snapshot) => {
                  const thread = snapshot?.threads.find((item) => item.id === result.threadId)
                  if (thread === undefined) {
                    return loop({ ...result.state, status: "That Thread is no longer available." })
                  }
                  return selectPrompt(
                    shell,
                    `Archive · ${thread.name}`,
                    [
                      {
                        name: "Cancel",
                        description: "Keep this Thread active",
                        value: false,
                      },
                      {
                        name: "Archive",
                        description: "Stop its terminal and move it out of the active list",
                        value: true,
                      },
                    ],
                    0,
                  ).pipe(
                    Effect.flatMap((confirmed) => {
                      if (confirmed.type === "quit") {
                        return Effect.succeed({
                          type: "quit",
                          selectedThreadId: result.threadId,
                        } as const)
                      }
                      if (confirmed.type === "cancel" || !confirmed.value) {
                        return loop({ ...result.state, status: "Archive cancelled." })
                      }
                      return Effect.succeed({
                        type: "archiveThread",
                        threadId: result.threadId,
                      } as const)
                    }),
                  )
                }),
              )
            }

            return Ref.get(options.snapshotRef).pipe(
              Effect.flatMap((snapshot) => {
                if (snapshot === undefined) {
                  return loop({
                    ...result.state,
                    status: "Wait for the App Server before creating.",
                  })
                }
                const preferredProjectId = View.projectIdForSelection(
                  snapshot,
                  result.state.selectedThreadId,
                )
                return runCreateThreadWizard(shell, snapshot, preferredProjectId).pipe(
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
                      type: "createThread",
                      input: created.input,
                      selectedThreadId: result.state.selectedThreadId,
                    } as const)
                  }),
                )
              }),
            )
          }),
        )

      return yield* loop(options.initial)
    }),
  )

export const run = (options: ManagerOptions): Effect.Effect<ManagerResult, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const renderer = yield* acquireRenderer
      return yield* runWithRenderer(renderer, options)
    }),
  )
