import { InputRenderableEvents, type KeyEvent } from "@opentui/core"
import { Effect, Queue } from "effect"
import type * as Scope from "effect/Scope"
import type * as AppServer from "./appServer.ts"
import * as DirectoryCompletion from "./directoryCompletion.ts"
import * as OpenTuiApp from "./openTuiApp.ts"

// The picker keeps text editing focused while a SelectRenderable presents
// server-host directory completions. Listings are cached per parent and stale
// responses only populate the cache; the current input always chooses what is
// rendered.

export type Result =
  | { readonly type: "value"; readonly value: string }
  | { readonly type: "cancel" }
  | { readonly type: "quit" }

export type ListDirectory = (path?: string) => Effect.Effect<AppServer.DirectoryListing, unknown>

type Event =
  | { readonly type: "input"; readonly value: string }
  | { readonly type: "submit"; readonly value: string }
  | { readonly type: "complete" }
  | { readonly type: "move"; readonly direction: "up" | "down" }
  | { readonly type: "cancel" }
  | { readonly type: "quit" }
  | {
      readonly type: "listed"
      readonly requestedPath: string | undefined
      readonly listing: AppServer.DirectoryListing
    }
  | {
      readonly type: "listFailed"
      readonly requestedPath: string | undefined
      readonly message: string
    }

const requestKey = (path: string | undefined): string => path ?? ""

const describeError = (error: unknown): string =>
  error instanceof Error && error.message !== "" ? error.message : String(error)

export const run = (
  shell: OpenTuiApp.AppShell,
  listDirectory: ListDirectory,
): Effect.Effect<Result> =>
  Effect.scoped(
    Effect.gen(function* () {
      const renderer = shell.renderer
      const events = yield* Queue.unbounded<Event>()
      const view = OpenTuiApp.makeFormView(shell, "directory-prompt-view")
      const label = OpenTuiApp.makePromptLabel(
        shell,
        "directory-prompt-label",
        "Directory on the App Server host (~ is its home)",
      )
      const input = OpenTuiApp.makeInput(shell, {
        id: "directory-prompt-input",
        placeholder: "/path/to/project or ~/project",
      })
      const select = OpenTuiApp.makeSelect(shell, {
        id: "directory-prompt-options",
        items: [],
        wrapSelection: true,
      })
      const empty = OpenTuiApp.makeMessage(shell, "directory-prompt-empty")
      view.add(label)
      view.add(input)
      view.add(select)
      view.add(empty)
      yield* OpenTuiApp.mountView(shell, view)
      OpenTuiApp.update(shell, {
        title: "New Project · Directory",
        status: "",
        help: "Type to filter  ·  ↑/↓ choose  ·  Tab complete  ·  Enter create\nEsc cancel  ·  Ctrl-C quit",
      })

      const browser = {
        home: undefined as string | undefined,
        cache: new Map<string, AppServer.DirectoryListing>(),
        pending: new Set<string>(),
        suggestions: [] as Array<AppServer.DirectoryEntry>,
      }

      const clearSuggestions = (message: string): void => {
        browser.suggestions.splice(0)
        select.options = []
        select.visible = false
        empty.visible = true
        empty.content = message
      }

      const renderSuggestions = (value: string, listing: AppServer.DirectoryListing): void => {
        if (browser.home === undefined) {
          clearSuggestions("Loading the App Server home…")
          return
        }
        const suggestions = DirectoryCompletion.suggestions(value, browser.home, listing)
        browser.suggestions.splice(0, browser.suggestions.length, ...suggestions)
        select.options = suggestions.map((entry) => ({
          name: entry.name + "/",
          description: entry.path,
        }))
        select.visible = suggestions.length > 0
        empty.visible = suggestions.length === 0
        empty.content =
          value.trim() === ""
            ? "No directories in the App Server home."
            : "No matching directories. You can still submit the typed path."
        if (suggestions.length > 0) select.setSelectedIndex(0)
      }

      const load = (requestedPath: string | undefined): Effect.Effect<void, never, Scope.Scope> => {
        const key = requestKey(requestedPath)
        if (browser.pending.has(key)) return Effect.void
        browser.pending.add(key)
        return Effect.forkScoped(
          listDirectory(requestedPath).pipe(
            Effect.map((listing): Event => ({ type: "listed", requestedPath, listing })),
            Effect.catch((error) =>
              Effect.succeed<Event>({
                type: "listFailed",
                requestedPath,
                message: describeError(error),
              }),
            ),
            Effect.flatMap((event) => Queue.offer(events, event)),
          ),
        ).pipe(Effect.asVoid)
      }

      const refresh = (value: string): Effect.Effect<void, never, Scope.Scope> => {
        OpenTuiApp.setStatus(shell, "")
        const target = DirectoryCompletion.listingPath(value, browser.home)
        if (target === undefined) {
          clearSuggestions(
            browser.home === undefined && value.trim() === ""
              ? "Loading the App Server home…"
              : "Type an absolute path or a path beginning with ~/.",
          )
          return Effect.void
        }
        const cached = browser.cache.get(target)
        if (cached !== undefined) {
          renderSuggestions(value, cached)
          return Effect.void
        }
        clearSuggestions(`Loading ${target}…`)
        return load(target)
      }

      const onInput = (value: string) =>
        Queue.offerUnsafe(events, { type: "input", value } as const)
      const onEnter = (value: string) =>
        Queue.offerUnsafe(events, { type: "submit", value } as const)
      const onKey = (key: KeyEvent) => {
        if (key.ctrl && key.name === "c") {
          key.preventDefault()
          Queue.offerUnsafe(events, { type: "quit" })
          return
        }
        if (key.name === "escape") {
          key.preventDefault()
          Queue.offerUnsafe(events, { type: "cancel" })
          return
        }
        if (key.name === "tab") {
          key.preventDefault()
          Queue.offerUnsafe(events, { type: "complete" })
          return
        }
        if (key.name === "up" || key.name === "down") {
          key.preventDefault()
          Queue.offerUnsafe(events, { type: "move", direction: key.name })
        }
      }
      input.on(InputRenderableEvents.INPUT, onInput)
      input.on(InputRenderableEvents.ENTER, onEnter)
      renderer.keyInput.on("keypress", onKey)
      yield* Effect.addFinalizer(() =>
        Effect.sync(() => {
          input.off(InputRenderableEvents.INPUT, onInput)
          input.off(InputRenderableEvents.ENTER, onEnter)
          renderer.keyInput.off("keypress", onKey)
        }),
      )
      input.focus()

      clearSuggestions("Loading the App Server home…")
      yield* load(undefined)

      while (true) {
        const event = yield* Queue.take(events)
        if (event.type === "quit" || event.type === "cancel") return event
        if (event.type === "input") {
          yield* refresh(event.value)
          continue
        }
        if (event.type === "move") {
          if (event.direction === "up") select.moveUp()
          else select.moveDown()
          continue
        }
        if (event.type === "complete") {
          const suggestion = browser.suggestions[select.getSelectedIndex()]
          if (suggestion !== undefined && browser.home !== undefined) {
            input.value = DirectoryCompletion.completedInput(suggestion.path, browser.home)
          }
          continue
        }
        if (event.type === "submit") {
          const resolved = DirectoryCompletion.resolveInput(event.value, browser.home)
          if (resolved !== undefined) return { type: "value", value: resolved }
          OpenTuiApp.setStatus(
            shell,
            event.value.trim().startsWith("~") && browser.home === undefined
              ? "The App Server home is unavailable; enter an absolute path."
              : "Enter an absolute path beginning with / or ~/.",
          )
          continue
        }

        const key = requestKey(event.requestedPath)
        browser.pending.delete(key)
        if (event.type === "listed") {
          browser.cache.set(key, event.listing)
          browser.cache.set(event.listing.path, event.listing)
          if (event.requestedPath === undefined) browser.home = event.listing.path
          yield* refresh(input.value)
          continue
        }

        const target = DirectoryCompletion.listingPath(input.value, browser.home)
        if (event.requestedPath === undefined && target !== undefined) {
          yield* refresh(input.value)
          continue
        }
        if (
          target === event.requestedPath ||
          (event.requestedPath === undefined && target === undefined)
        ) {
          OpenTuiApp.setStatus(shell, `Could not list directories: ${event.message}`)
          clearSuggestions("You can still submit an absolute path.")
        }
      }
    }),
  )
