import {
  BoxRenderable,
  fg,
  InputRenderable,
  StyledText,
  TextRenderable,
  type CliRenderer,
  type Renderable,
} from "@opentui/core"
import { Effect } from "effect"
import { StyledSelectRenderable } from "./styledSelect.ts"

// One persistent application frame owns the content panel, status, help, and
// visual tokens. Individual workflows replace only the panel body,
// so the Thread list and creation steps remain screens of the same TUI.

const colors = {
  border: "#334155",
  title: "#93c5fd",
  text: "#e2e8f0",
  muted: "#64748b",
  description: "#94a3b8",
  status: "#fbbf24",
  selection: "#73737340",
  selectedDescription: "#dbeafe",
  threadNew: "#2dd4bf",
  threadIdle: "#64748b",
  threadAttention: "#fb7185",
  threadUnknown: "#94a3b8",
} as const

const runningPulseColors = [
  "#b45309",
  "#c26708",
  "#d17b07",
  "#e08f09",
  "#efa414",
  "#fbbf24",
  "#efa414",
  "#e08f09",
  "#d17b07",
  "#c26708",
] as const

export type ThreadStatusTone = "attention" | "running" | "new" | "idle" | "unknown"

const threadStatusColor = (tone: ThreadStatusTone, animationFrame: number): string => {
  if (tone === "attention") return colors.threadAttention
  if (tone === "new") return colors.threadNew
  if (tone === "idle") return colors.threadIdle
  if (tone === "unknown") return colors.threadUnknown
  return runningPulseColors[animationFrame % runningPulseColors.length] ?? colors.status
}

export const styledThreadName = (
  marker: string,
  tone: ThreadStatusTone,
  name: string,
  animationFrame: number,
): StyledText =>
  new StyledText([
    fg(threadStatusColor(tone, animationFrame))(marker),
    fg(colors.text)(`  ${name}`),
  ])

export interface AppShell {
  readonly renderer: CliRenderer
  readonly screen: BoxRenderable
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
    visible: false,
  })
  const help = new TextRenderable(renderer, {
    id: "atc-help",
    height: 1,
    content: "",
    fg: colors.muted,
    visible: false,
  })
  screen.add(panel)
  screen.add(status)
  screen.add(help)
  return { renderer, screen, panel, status, help }
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
): StyledSelectRenderable =>
  new StyledSelectRenderable(shell.renderer, {
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

export const makeMessage = (shell: AppShell, id: string): TextRenderable =>
  new TextRenderable(shell.renderer, {
    id,
    width: "100%",
    height: "100%",
    content: "",
    fg: colors.description,
  })

export const setStatus = (shell: AppShell, status: string): void => {
  shell.status.content = status
  shell.status.visible = status !== ""
}

export const setHelp = (shell: AppShell, help: string): void => {
  shell.help.content = help
  shell.help.height = Math.max(1, help.split("\n").length)
  shell.help.visible = help !== ""
}

export const update = (
  shell: AppShell,
  content: {
    readonly title: string
    readonly status?: string | undefined
    readonly help: string
  },
): void => {
  shell.panel.title = ` ${content.title} `
  setStatus(shell, content.status ?? "")
  setHelp(shell, content.help)
}
