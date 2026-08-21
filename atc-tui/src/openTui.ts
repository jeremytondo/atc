import {
  createCliRenderer,
  InputRenderableEvents,
  SelectRenderableEvents,
  type CliRenderer,
  type KeyEvent,
} from "@opentui/core"
import { Effect, Queue, Ref } from "effect"
import * as AppServer from "./appServer.ts"
import * as DirectoryPrompt from "./directoryPrompt.ts"
import * as OpenTuiApp from "./openTuiApp.ts"
import * as View from "./view.ts"

// OpenTUI owns only the interactive manager surface. Callbacks publish typed
// events into Effect queues, and the read-only directory browser receives its
// API capability from the application coordinator. One renderer spans manager
// actions and is destroyed only before zmx inherits the real TTY.

export type ManagerSection = "threads" | "archived" | "projects"

export interface ManagerState {
  readonly section?: ManagerSection | undefined
  readonly selectedThreadId?: string | undefined
  readonly selectedArchivedThreadId?: string | undefined
  readonly selectedProjectId?: string | undefined
  readonly status?: string | undefined
}

export type ManagerResult =
  | { readonly type: "quit" }
  | { readonly type: "attach"; readonly threadId: string; readonly state: ManagerState }
  | {
      readonly type: "createThread"
      readonly input: AppServer.CreateThreadInput
      readonly state: ManagerState
    }
  | {
      readonly type: "createProject"
      readonly input: AppServer.CreateProjectInput
      readonly state: ManagerState
    }
  | { readonly type: "archiveThread"; readonly threadId: string; readonly state: ManagerState }
  | { readonly type: "unarchiveThread"; readonly threadId: string; readonly state: ManagerState }
  | {
      readonly type: "updateProject"
      readonly projectId: string
      readonly input: AppServer.UpdateProjectInput
      readonly state: ManagerState
    }
  | { readonly type: "deleteProject"; readonly projectId: string; readonly state: ManagerState }

export type ManagerAction = Exclude<ManagerResult, { readonly type: "quit" }>

type ManagerAttachExit = {
  readonly type: "attach"
  readonly terminal: AppServer.Terminal
  readonly state: ManagerState
}

export type ManagerExit = { readonly type: "quit" } | ManagerAttachExit

export type ManagerTransition =
  { readonly type: "continue"; readonly state: ManagerState } | ManagerAttachExit

export type RunAction = (action: ManagerAction) => Effect.Effect<ManagerTransition, never>

export interface ManagerOptions {
  readonly endpoint: URL
  readonly listDirectory: DirectoryPrompt.ListDirectory
  readonly snapshotRef: Ref.Ref<AppServer.Snapshot | undefined>
  readonly reachabilityRef: Ref.Ref<View.Reachability>
  readonly backgroundStatusRef: Ref.Ref<string | undefined>
  readonly uiUpdates: Queue.Dequeue<void>
  readonly refreshRequests: Queue.Enqueue<void>
  readonly initial: ManagerState
}

type MainResult =
  | { readonly type: "quit" }
  | { readonly type: "attach"; readonly threadId: string; readonly state: ManagerState }
  | {
      readonly type: "switchSection"
      readonly section: ManagerSection
      readonly state: ManagerState
    }
  | { readonly type: "newThread"; readonly state: ManagerState }
  | { readonly type: "newProject"; readonly state: ManagerState }
  | { readonly type: "archiveThread"; readonly threadId: string; readonly state: ManagerState }
  | { readonly type: "unarchiveThread"; readonly threadId: string; readonly state: ManagerState }
  | { readonly type: "renameProject"; readonly projectId: string; readonly state: ManagerState }
  | { readonly type: "deleteProject"; readonly projectId: string; readonly state: ManagerState }

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
  | { readonly type: "attach"; readonly id: string }
  | { readonly type: "newThread" }
  | { readonly type: "newProject" }
  | { readonly type: "archiveThread"; readonly id: string }
  | { readonly type: "unarchiveThread"; readonly id: string }
  | { readonly type: "renameProject"; readonly id: string }
  | { readonly type: "deleteProject"; readonly id: string }
  | { readonly type: "switchSection"; readonly section: ManagerSection }
  | { readonly type: "refresh" }
  | { readonly type: "quit" }

const nextSection = (section: ManagerSection): ManagerSection =>
  section === "threads" ? "archived" : section === "archived" ? "projects" : "threads"

const runMainScreen = (
  shell: OpenTuiApp.AppShell,
  options: ManagerOptions,
  initial: ManagerState,
): Effect.Effect<MainResult, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const renderer = shell.renderer
      const section = initial.section ?? "threads"
      const events = yield* Queue.unbounded<MainEvent>()
      const itemIdsByIndex: Array<string> = []
      const view = OpenTuiApp.makeView(shell, "manager-view")
      const list = OpenTuiApp.makeSelect(shell, { id: "manager-list", items: [] })
      const empty = OpenTuiApp.makeMessage(shell, "manager-empty")
      view.add(list)
      view.add(empty)
      yield* OpenTuiApp.mountView(shell, view)

      const selectedId = () => itemIdsByIndex[list.getSelectedIndex()]
      const selectionState = (
        state: ManagerState,
        id: string | undefined,
        status?: string | undefined,
      ): ManagerState => {
        if (section === "archived") {
          return { ...state, section, selectedArchivedThreadId: id, status }
        }
        if (section === "projects") {
          return { ...state, section, selectedProjectId: id, status }
        }
        return { ...state, section, selectedThreadId: id, status }
      }
      const offerSelected = (
        type: "archiveThread" | "unarchiveThread" | "renameProject" | "deleteProject",
      ) => {
        const id = selectedId()
        if (id !== undefined) Queue.offerUnsafe(events, { type, id })
      }
      const onKey = (key: KeyEvent) => {
        if (key.ctrl && key.name === "c") {
          Queue.offerUnsafe(events, { type: "quit" })
          return
        }
        if (key.name === "tab") {
          Queue.offerUnsafe(events, { type: "switchSection", section: nextSection(section) })
          return
        }
        if (key.name === "1" || key.name === "2" || key.name === "3") {
          const target = key.name === "1" ? "threads" : key.name === "2" ? "archived" : "projects"
          Queue.offerUnsafe(events, { type: "switchSection", section: target })
          return
        }
        if (key.ctrl && key.name === "n") {
          Queue.offerUnsafe(events, { type: "newThread" })
          return
        }
        if (key.ctrl && key.name === "p") {
          Queue.offerUnsafe(events, { type: "newProject" })
          return
        }
        if (section === "threads" && key.name === "a") {
          offerSelected("archiveThread")
          return
        }
        if (section === "archived" && key.name === "u") {
          offerSelected("unarchiveThread")
          return
        }
        if (section === "projects" && key.name === "e") {
          offerSelected("renameProject")
          return
        }
        if (section === "projects" && key.name === "d") {
          offerSelected("deleteProject")
          return
        }
        if (key.name === "q") {
          Queue.offerUnsafe(events, { type: "quit" })
          return
        }
        if (key.name === "r") Queue.offerUnsafe(events, { type: "refresh" })
      }
      const onItemSelected = (index: number) => {
        const id = itemIdsByIndex[index]
        if (id === undefined) return
        if (section === "threads") {
          Queue.offerUnsafe(events, { type: "attach", id })
          return
        }
        if (section === "archived") {
          Queue.offerUnsafe(events, { type: "unarchiveThread", id })
          return
        }
        Queue.offerUnsafe(events, { type: "renameProject", id })
      }
      renderer.keyInput.on("keypress", onKey)
      list.on(SelectRenderableEvents.ITEM_SELECTED, onItemSelected)
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => {
          renderer.keyInput.off("keypress", onKey)
          list.off(SelectRenderableEvents.ITEM_SELECTED, onItemSelected)
        }),
      )
      list.focus()

      const render = (state: ManagerState): Effect.Effect<void> =>
        Effect.gen(function* () {
          const snapshot = yield* Ref.get(options.snapshotRef)
          const reachability = yield* Ref.get(options.reachabilityRef)
          const backgroundStatus = yield* Ref.get(options.backgroundStatusRef)
          const items =
            section === "projects"
              ? View.projectOptions(snapshot).map((item) => ({
                  id: item.projectId,
                  name: item.name,
                  description: item.description,
                }))
              : View.threadOptions(snapshot, section === "archived").map((item) => ({
                  id: item.threadId,
                  name: item.name,
                  description: item.description,
                }))
          const selected =
            section === "projects"
              ? View.normalizeProjectSelection(snapshot, state.selectedProjectId)
              : View.normalizeSelection(
                  snapshot,
                  section === "archived" ? state.selectedArchivedThreadId : state.selectedThreadId,
                  section === "archived",
                )
          const selectedIndex = Math.max(
            0,
            items.findIndex((item) => item.id === selected),
          )

          itemIdsByIndex.splice(0, itemIdsByIndex.length, ...items.map((item) => item.id))
          list.options = items.map((item) => ({
            name: item.name,
            description: item.description,
          }))
          if (items.length > 0 && list.getSelectedIndex() !== selectedIndex) {
            list.setSelectedIndex(selectedIndex)
          }
          list.visible = items.length > 0
          empty.visible = items.length === 0
          if (items.length > 0 && renderer.currentFocusedRenderable !== list) list.focus()
          empty.content =
            snapshot === undefined
              ? "Waiting for the App Server…"
              : section === "projects"
                ? "No Projects yet. Press Ctrl-P to create one."
                : section === "archived"
                  ? "No archived Threads."
                  : snapshot.projects.length === 0
                    ? "No Projects yet. Press Ctrl-P to create one."
                    : "No active Threads. Press Ctrl-N to create one."
          const fetched =
            snapshot === undefined ? "" : `  ·  synced ${snapshot.fetchedAt.toLocaleTimeString()}`
          const title =
            section === "threads"
              ? `Active Threads (${items.length})`
              : section === "archived"
                ? `Archived Threads (${items.length})`
                : `Projects (${items.length})`
          const help =
            section === "threads"
              ? "Tab/1-3 views  ·  ↑/↓ navigate  ·  Enter attach  ·  a archive  ·  Ctrl-N new\nCtrl-P new project  ·  r refresh  ·  q quit  ·  zmx Ctrl-\\ returns"
              : section === "archived"
                ? "Tab/1-3 views  ·  ↑/↓ navigate  ·  Enter or u unarchive\nCtrl-N new thread  ·  Ctrl-P new project  ·  r refresh  ·  q quit"
                : "Tab/1-3 views  ·  ↑/↓ navigate  ·  Enter or e rename  ·  d delete\nCtrl-P new project  ·  Ctrl-N new thread  ·  r refresh  ·  q quit"
          OpenTuiApp.update(shell, {
            subtitle:
              options.endpoint.origin + `  ·  ${View.connectionLabel(reachability)}` + fetched,
            title,
            status: state.status ?? backgroundStatus,
            help,
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
            if (event.type === "update") return loop(selectionState(state, selectedId()))
            if (event.type === "refresh") {
              return Queue.offer(options.refreshRequests, void 0).pipe(
                Effect.andThen(loop(selectionState(state, selectedId(), "Refreshing…"))),
              )
            }
            if (event.type === "switchSection") {
              return Effect.succeed({
                type: "switchSection",
                section: event.section,
                state,
              } as const)
            }
            if (event.type === "newThread" || event.type === "newProject") {
              return Effect.succeed({
                type: event.type,
                state: selectionState(state, selectedId()),
              } as const)
            }
            if (event.type === "quit") return Effect.succeed({ type: "quit" } as const)
            if (event.type === "attach") {
              return Effect.succeed({
                type: "attach",
                threadId: event.id,
                state: selectionState(state, event.id),
              } as const)
            }
            if (event.type === "renameProject" || event.type === "deleteProject") {
              return Effect.succeed({
                type: event.type,
                projectId: event.id,
                state: selectionState(state, event.id),
              } as const)
            }
            return Effect.succeed({
              type: event.type,
              threadId: event.id,
              state: selectionState(state, event.id),
            } as const)
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

const runCreateProjectWizard = (
  shell: OpenTuiApp.AppShell,
  listDirectory: DirectoryPrompt.ListDirectory,
): Effect.Effect<ProjectWizardResult> =>
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

    const directory = yield* DirectoryPrompt.run(shell, listDirectory)
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

const runManager = (
  shell: OpenTuiApp.AppShell,
  options: ManagerOptions,
  initial: ManagerState,
): Effect.Effect<ManagerResult, unknown> => {
  const loop = (state: ManagerState): Effect.Effect<ManagerResult, unknown> =>
    runMainScreen(shell, options, state).pipe(
      Effect.flatMap((result): Effect.Effect<ManagerResult, unknown> => {
        if (result.type === "quit" || result.type === "attach") {
          return Effect.succeed(result)
        }
        if (result.type === "switchSection") {
          return loop({ ...result.state, section: result.section, status: undefined })
        }
        if (result.type === "archiveThread" || result.type === "unarchiveThread") {
          return Effect.succeed(result)
        }
        if (result.type === "newProject") {
          return runCreateProjectWizard(shell, options.listDirectory).pipe(
            Effect.flatMap((created): Effect.Effect<ManagerResult, unknown> => {
              if (created.type === "quit") return Effect.succeed({ type: "quit" } as const)
              if (created.type === "cancel") {
                return loop({ ...result.state, status: created.status })
              }
              return Effect.succeed({
                type: "createProject",
                input: created.input,
                state: result.state,
              } as const)
            }),
          )
        }
        if (result.type === "renameProject") {
          return Ref.get(options.snapshotRef).pipe(
            Effect.flatMap((snapshot): Effect.Effect<ManagerResult, unknown> => {
              const project = snapshot?.projects.find((item) => item.id === result.projectId)
              if (project === undefined) {
                return loop({ ...result.state, status: "That Project is no longer available." })
              }
              return textPrompt(shell, {
                title: `Rename Project · ${project.name}`,
                label: "New project name",
                placeholder: project.name,
                validate: (value) => (value === "" ? "Project name is required." : undefined),
              }).pipe(
                Effect.flatMap((renamed): Effect.Effect<ManagerResult, unknown> => {
                  if (renamed.type === "quit") return Effect.succeed({ type: "quit" } as const)
                  if (renamed.type === "cancel") {
                    return loop({ ...result.state, status: "Project rename cancelled." })
                  }
                  return Effect.succeed({
                    type: "updateProject",
                    projectId: result.projectId,
                    input: { name: renamed.value },
                    state: result.state,
                  } as const)
                }),
              )
            }),
          )
        }
        if (result.type === "deleteProject") {
          return Ref.get(options.snapshotRef).pipe(
            Effect.flatMap((snapshot): Effect.Effect<ManagerResult, unknown> => {
              const project = snapshot?.projects.find((item) => item.id === result.projectId)
              if (project === undefined) {
                return loop({ ...result.state, status: "That Project is no longer available." })
              }
              const threadCount =
                snapshot?.threads.filter((thread) => thread.projectId === project.id).length ?? 0
              const threadNoun = threadCount === 1 ? "Thread" : "Threads"
              return selectPrompt(
                shell,
                `Delete Project · ${project.name}`,
                [
                  {
                    name: "Cancel",
                    description: "Keep this Project and everything it owns",
                    value: false,
                  },
                  {
                    name: "Delete permanently",
                    description: `Delete ${threadCount} ${threadNoun} and their Terminals; keep the directory`,
                    value: true,
                  },
                ],
                0,
              ).pipe(
                Effect.flatMap((confirmed): Effect.Effect<ManagerResult, unknown> => {
                  if (confirmed.type === "quit") return Effect.succeed({ type: "quit" } as const)
                  if (confirmed.type === "cancel" || !confirmed.value) {
                    return loop({ ...result.state, status: "Project deletion cancelled." })
                  }
                  return Effect.succeed({
                    type: "deleteProject",
                    projectId: result.projectId,
                    state: result.state,
                  } as const)
                }),
              )
            }),
          )
        }

        return Ref.get(options.snapshotRef).pipe(
          Effect.flatMap((snapshot): Effect.Effect<ManagerResult, unknown> => {
            if (snapshot === undefined) {
              return loop({
                ...result.state,
                status: "Wait for the App Server before creating.",
              })
            }
            const selectedThreadId =
              result.state.section === "archived"
                ? result.state.selectedArchivedThreadId
                : result.state.selectedThreadId
            const preferredProjectId =
              result.state.section === "projects"
                ? result.state.selectedProjectId
                : View.projectIdForSelection(snapshot, selectedThreadId)
            return runCreateThreadWizard(shell, snapshot, preferredProjectId).pipe(
              Effect.flatMap((created): Effect.Effect<ManagerResult, unknown> => {
                if (created.type === "quit") return Effect.succeed({ type: "quit" } as const)
                if (created.type === "cancel") {
                  return loop({ ...result.state, status: created.status })
                }
                return Effect.succeed({
                  type: "createThread",
                  input: created.input,
                  state: result.state,
                } as const)
              }),
            )
          }),
        )
      }),
    )

  return loop(initial)
}

const pendingStatus = (action: ManagerAction): string => {
  switch (action.type) {
    case "attach":
      return "Opening Thread…"
    case "createThread":
      return "Creating Thread…"
    case "createProject":
      return "Creating Project…"
    case "archiveThread":
      return "Archiving Thread…"
    case "unarchiveThread":
      return "Restoring Thread…"
    case "updateProject":
      return "Renaming Project…"
    case "deleteProject":
      return "Deleting Project…"
  }
}

const runPendingAction = (
  shell: OpenTuiApp.AppShell,
  status: string,
  action: Effect.Effect<ManagerTransition>,
): Effect.Effect<ManagerTransition> =>
  Effect.scoped(
    Effect.gen(function* () {
      const message = OpenTuiApp.makeMessage(shell, "manager-pending")
      message.content = status
      yield* OpenTuiApp.mountView(shell, message)
      OpenTuiApp.update(shell, {
        title: "Working",
        status,
        help: "Please wait…",
      })
      return yield* action
    }),
  )

export const runWithRenderer = (
  renderer: CliRenderer,
  options: ManagerOptions,
): Effect.Effect<ManagerResult, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const shell = OpenTuiApp.make(renderer)
      yield* OpenTuiApp.mount(shell)
      return yield* runManager(shell, options, options.initial)
    }),
  )

export const runSessionWithRenderer = (
  renderer: CliRenderer,
  options: ManagerOptions,
  runAction: RunAction,
): Effect.Effect<ManagerExit, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const shell = OpenTuiApp.make(renderer)
      yield* OpenTuiApp.mount(shell)

      const loop = (state: ManagerState): Effect.Effect<ManagerExit, unknown> =>
        runManager(shell, options, state).pipe(
          Effect.flatMap((result): Effect.Effect<ManagerExit, unknown> => {
            if (result.type === "quit") return Effect.succeed(result)
            return runPendingAction(shell, pendingStatus(result), runAction(result)).pipe(
              Effect.flatMap((transition) =>
                transition.type === "continue"
                  ? loop(transition.state)
                  : Effect.succeed(transition),
              ),
            )
          }),
        )

      return yield* loop(options.initial)
    }),
  )

export const run = (
  options: ManagerOptions,
  runAction: RunAction,
): Effect.Effect<ManagerExit, unknown> =>
  Effect.scoped(
    Effect.gen(function* () {
      const renderer = yield* acquireRenderer
      return yield* runSessionWithRenderer(renderer, options, runAction)
    }),
  )
