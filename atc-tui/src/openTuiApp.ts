import {
  BoxRenderable,
  InputRenderable,
  SelectRenderable,
  TabSelectRenderable,
  TextRenderable,
  type CliRenderer,
  type Renderable,
} from "@opentui/core"
import { Effect } from "effect"

// One persistent application frame owns the ATC header, content panel, status,
// help, and visual tokens. Individual workflows replace only the panel body,
// so the Thread list and creation steps remain screens of the same TUI.

const colors = {
  border: "#334155",
  title: "#93c5fd",
  text: "#e2e8f0",
  muted: "#64748b",
  description: "#94a3b8",
  status: "#fbbf24",
  selection: "#293241",
  selectedDescription: "#dbeafe",
} as const

export interface AppShell {
  readonly renderer: CliRenderer
  readonly screen: BoxRenderable
  readonly header: TextRenderable
  readonly panel: BoxRenderable
  readonly status: TextRenderable
  readonly help: TextRenderable
}

export const make = (renderer: CliRenderer): AppShell => {
  const screen = new BoxRenderable(renderer, {
    id: "atc-app",
    width: "100%",
    height: "100%",
    padding: 1,
    gap: 1,
    flexDirection: "column",
  })
  const header = new TextRenderable(renderer, {
    id: "atc-header",
    height: 2,
    content: "ATC",
    fg: colors.text,
  })
  const panel = new BoxRenderable(renderer, {
    id: "atc-panel",
    title: " ATC ",
    titleColor: colors.title,
    border: true,
    borderStyle: "rounded",
    borderColor: colors.border,
    flexGrow: 1,
    padding: 1,
  })
  const status = new TextRenderable(renderer, {
    id: "atc-status",
    height: 1,
    content: "",
    fg: colors.status,
  })
  const help = new TextRenderable(renderer, {
    id: "atc-help",
    height: 2,
    content: "",
    fg: colors.muted,
  })
  screen.add(header)
  screen.add(panel)
  screen.add(status)
  screen.add(help)
  return { renderer, screen, header, panel, status, help }
}

export const mount = (shell: AppShell) =>
  Effect.acquireRelease(
    Effect.sync(() => {
      shell.renderer.root.add(shell.screen)
      return shell
    }),
    (owned) =>
      Effect.try({
        try: () => {
          if (owned.screen.parent !== null) owned.renderer.root.remove(owned.screen)
          owned.screen.destroyRecursively()
        },
        catch: () => undefined,
      }).pipe(Effect.ignore),
  )

export const makeView = (shell: AppShell, id: string): BoxRenderable =>
  new BoxRenderable(shell.renderer, {
    id,
    width: "100%",
    height: "100%",
  })

export const makeFormView = (shell: AppShell, id: string): BoxRenderable =>
  new BoxRenderable(shell.renderer, {
    id,
    width: "100%",
    height: "100%",
    flexDirection: "column",
    gap: 1,
  })

export const makePromptLabel = (shell: AppShell, id: string, content: string): TextRenderable =>
  new TextRenderable(shell.renderer, {
    id,
    height: 2,
    content,
    fg: colors.description,
  })

export const makeInput = (
  shell: AppShell,
  options: {
    readonly id: string
    readonly placeholder: string
    readonly value?: string | undefined
  },
): InputRenderable =>
  new InputRenderable(shell.renderer, {
    id: options.id,
    width: "100%",
    value: options.value ?? "",
    placeholder: options.placeholder,
    textColor: colors.text,
    focusedTextColor: colors.text,
    placeholderColor: colors.muted,
  })

export const mountView = (shell: AppShell, view: Renderable) =>
  Effect.acquireRelease(
    Effect.sync(() => {
      shell.panel.add(view)
      return view
    }),
    (owned) =>
      Effect.try({
        try: () => {
          if (owned.parent !== null) shell.panel.remove(owned)
          owned.destroyRecursively()
        },
        catch: () => undefined,
      }).pipe(Effect.ignore),
  )

export const makeSelect = (
  shell: AppShell,
  options: {
    readonly id: string
    readonly items: ReadonlyArray<{ readonly name: string; readonly description: string }>
    readonly selectedIndex?: number | undefined
    readonly wrapSelection?: boolean | undefined
  },
): SelectRenderable =>
  new SelectRenderable(shell.renderer, {
    id: options.id,
    width: "100%",
    height: "100%",
    options: options.items.map((item) => ({ ...item })),
    ...(options.selectedIndex === undefined ? {} : { selectedIndex: options.selectedIndex }),
    wrapSelection: options.wrapSelection ?? false,
    showDescription: true,
    showScrollIndicator: true,
    selectedBackgroundColor: colors.selection,
    selectedTextColor: "#ffffff",
    selectedDescriptionColor: colors.selectedDescription,
    descriptionColor: colors.description,
  })

export const makeTabs = (
  shell: AppShell,
  options: {
    readonly id: string
    readonly items: ReadonlyArray<{ readonly name: string }>
    readonly selectedIndex: number
  },
): TabSelectRenderable => {
  const tabs = new TabSelectRenderable(shell.renderer, {
    id: options.id,
    width: "100%",
    options: options.items.map((item) => ({ ...item, description: "" })),
    tabWidth: 20,
    backgroundColor: "transparent",
    textColor: colors.description,
    selectedBackgroundColor: colors.selection,
    selectedTextColor: "#ffffff",
    showDescription: false,
    showScrollArrows: false,
    showUnderline: true,
  })
  tabs.focusable = false
  tabs.setSelectedIndex(options.selectedIndex)
  return tabs
}

export const makeMessage = (shell: AppShell, id: string): TextRenderable =>
  new TextRenderable(shell.renderer, {
    id,
    width: "100%",
    height: "100%",
    content: "",
    fg: colors.description,
  })

export const update = (
  shell: AppShell,
  content: {
    readonly subtitle?: string | undefined
    readonly title: string
    readonly status?: string | undefined
    readonly help: string
  },
): void => {
  if (content.subtitle !== undefined) shell.header.content = `ATC\n${content.subtitle}`
  shell.panel.title = ` ${content.title} `
  shell.status.content = content.status ?? ""
  shell.help.content = content.help
}
