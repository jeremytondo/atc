# atc macOS UX Design Prompt

## Assignment

Design a native macOS experience for **atc**, an app for organizing and running
terminal-based development work on local or remote machines.

## Product Model

```text
Connection
└── Project
    └── Workspace
        ├── Sessions
        └── Terminals
```

- **Connection**: A named local or remote machine running atc.
- **Project**: A code project on one Connection with a default repository
  folder.
- **Workspace**: The place where a user works on one task within a Project. It
  uses the Project's default repository folder in v1.
- **Agent Session**: A terminal running Codex, Claude Code, or another agent. A
  Workspace can contain several Agent Sessions.
- **Terminal**: A named shell in the Workspace folder. A Workspace can contain
  several Terminals.

Workspaces should feel like persistent places for completing a task, not just
folders containing a loose list of terminal processes.

## Primary UX Direction

Design two main app surfaces:

### 1. Dashboard

The Dashboard opens at launch and helps users find, create, and resume work. It
should include:

- Projects from all configured Connections
- The Connection associated with each Project
- Workspaces grouped under their Project
- Clear actions to create a Project or Workspace
- An obvious way to open an existing Workspace
- Useful empty states for no Connections or no Projects

List Workspaces newest-created-first. Do not add a separate Recent Workspaces
area, activity feed, relative activity time, or server-side recency tracking in
v1. Workspace rows do not show runtime or activity indicators.

Use a restrained card/list hybrid: Connection sections contain compact Project
cards, and each Project card contains its Workspace rows. Show the Project
directory as secondary context, place New Workspace in the Project row, and use
a quieter empty Project row when no Workspaces exist. The Dashboard does not
need the Workspace sidebar.

Project-row creation, a current Workspace, and a future global New Workspace
command use the same creation flow. Contextual entry points preselect their
Project; a context-free command asks the user to choose one. Successful creation
opens the new Workspace.

### 2. Workspace

The Workspace is where users spend most of their time. Use a focused layout
with:

- A title area showing the Workspace name with lightweight Project and
  Connection context
- A way to return to the Dashboard
- A sidebar for switching between Sessions and Terminals
- A large content area for the selected terminal surface, or an empty Workspace
state when nothing is selected

Keep Workspace identity at the window level. The selected Session or Terminal
gets a compact content header with its name, Action label where applicable,
working directory, and process lifecycle; the terminal canvas receives the
remaining space.

The sidebar should contain:

- A **Sessions** section with a New Session action
- A **Terminals** section with a New Terminal action
- Clear selection and compact generic process-lifecycle indicators

Restore the last selected Session when it still exists. Otherwise show an empty
state with New Session and New Terminal actions. Switching surfaces should
feel immediate and preserve the sense that each process is still running.

Delete actions require confirmation. Session delete explains that it may stop a
Session and removes atc history; Workspace delete names the affected Workspace
and Session count; Project delete is available only after its Workspaces are
deleted. Every confirmation states that files are not touched.

Archive is reversible for Projects, Workspaces, and Sessions. Archived Sessions
are available through an Archived filter within their Workspace; Delete is the
permanent metadata-removal action.

## Screens to Design

1. **Populated Dashboard** — Projects across two Connections, with zero, one,
   and multiple Workspaces plus clear creation actions.
2. **Dashboard Empty State** — No Connection configured and one clear first
   step.
3. **Create Workspace** — Project, name, and Create and Open action.
4. **Workspace Empty State** — New Session and New Terminal actions.
5. **Session Active** — Agent, session name, and compact status.
6. **Terminal Active** — A named shell with easy Terminal creation and
   switching.
7. **New Session** — Choose an Agent Action, optionally name it,
   and start it using the server default Environment. Do not add an initial
   prompt field in this UI.
8. **New Terminal** — Start the default Interactive Shell or choose an Action
   from the Terminal creation flow, optionally name it, and use the server
   default Environment.

The server's existing initial-prompt and Session Input capabilities remain
available to server and CLI clients. This macOS rework does not add a prompt
composer or agent controls; normal typing in the attached terminal remains the
interactive path.

New Session and New Terminal are Workspace-scoped commands. Sidebar controls
and future shortcuts create directly in the active Workspace; without one,
these commands are disabled.

User-provided Session names override display defaults and need not be unique.
An unnamed Session shows its Agent Action label; an unnamed default-shell
Terminal shows `Terminal`; and an unnamed Action-launched Terminal shows its
Action label.

## Keyboard and Interaction Principles

- Support pointer and keyboard input equally well.
- Keep terminal typing primary; do not intercept bare keys globally.
- When the sidebar has focus, support arrow keys and Vim-style `j/k`.
- Account for a `Shift-Cmd-P` command palette and commands that focus the
  sidebar or active terminal.
- Keep important actions visible and use familiar macOS interaction patterns.

Do not define the complete shortcut map; leave room for a keyboard-first flow.

## Visual Direction

- Native macOS, not a web dashboard inside a desktop window
- Dark mode for the initial concepts
- Compact, calm, and highly legible
- Terminal content is the visual priority
- Clear hierarchy through typography, spacing, and indentation
- Restrained cards, borders, badges, and status color
- Familiar SF Symbols and native controls where appropriate
- Avoid the density and panel overload of a full IDE

Use Codex Desktop, T3 Code, and AGTerm as inspiration.

## Sample Content

Use consistent sample data across the designs:

- **MacBook Pro** — local Connection
  - **atc**
    - `workspace-ui`
    - `connection-reliability`
  - **dotfiles** — no Workspaces
- **Studio** — remote Connection
  - **website**
    - `homepage-refresh`

Inside `workspace-ui`:

- Agent Sessions
  - `Plan dashboard` — Codex, running
  - `Review workspace model` — Claude Code, ended
- Terminals
  - `Tests` — running
  - `Server` — running
