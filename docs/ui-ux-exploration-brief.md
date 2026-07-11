# atc macOS UX Design Prompt

## Assignment

Design a native macOS experience for **atc**, an app for organizing and running
terminal-based development work on local or remote machines.

## Product Model

```text
Connection
└── Project
    └── Workspace
        ├── Editor
        ├── Agent Sessions
        └── Terminals
```

- **Connection**: A named local or remote machine running atc.
- **Project**: A code project on one Connection with a default repository
  folder.
- **Workspace**: The place where a user works on one task within a Project. It
  may use the main checkout, a Git worktree, or a Jujutsu workspace.
- **Editor**: The Workspace's single terminal editor, initially Neovim. It
  starts automatically and is always easy to reopen.
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

Explore two Dashboard layouts:

1. A compact outline with Workspaces nested under Projects
2. A restrained card/list layout where each Project contains its Workspaces

Recommend the option that is easiest to scan and use from the keyboard. The
Dashboard does not need the Workspace sidebar.

### 2. Workspace

The Workspace is where users spend most of their time. Use a focused layout
with:

- A title area showing the Workspace name with lightweight Project and
  Connection context
- A way to return to the Dashboard
- A sidebar for switching between the Editor, Agent Sessions, and Terminals
- A large content area for the selected terminal surface

The sidebar should contain:

- A persistent **Editor** entry or button
- An **Agent Sessions** section with a New Agent Session action
- A **Terminals** section with a New Terminal action
- Clear selection and compact status indicators

The Editor is not a repeatable session. Give it a distinct, persistent entry
point. Switching surfaces should feel immediate and preserve the sense that
each process is still running.

## Screens to Design

1. **Populated Dashboard** — Projects across two Connections, with zero, one,
   and multiple Workspaces plus clear creation actions.
2. **Dashboard Empty State** — No Connection configured and one clear first
   step.
3. **Create Workspace** — Project, name, checkout strategy, and Create and Open
   action.
4. **Editor Active** — The default Workspace view, dominated by the Editor.
5. **Agent Session Active** — Agent, session name, and compact status.
6. **Terminal Active** — A named shell with easy Terminal creation and
   switching.
7. **New Agent Session** — Choose an agent, optionally name it, and start it.
8. **New Terminal** — Optionally name it and create it.

## Keyboard and Interaction Principles

- Support pointer and keyboard input equally well.
- Keep terminal/editor typing primary; do not intercept bare keys globally.
- When the sidebar has focus, support arrow keys and Vim-style `j/k`.
- Account for a `Shift-Cmd-P` command palette and commands that focus the
  sidebar or active terminal.
- Keep important actions visible and use familiar macOS interaction patterns.

Do not define the complete shortcut map; leave room for a keyboard-first flow.

## Visual Direction

- Native macOS, not a web dashboard inside a desktop window
- Dark mode for the initial concepts
- Compact, calm, and highly legible
- Terminal/editor content is the visual priority
- Clear hierarchy through typography, spacing, and indentation
- Restrained cards, borders, badges, and status color
- Familiar SF Symbols and native controls where appropriate
- Avoid the density and panel overload of a full IDE

Use Codex Desktop, T3 Code, and AGTerm as inspiration.

## Sample Content

Use consistent sample data across the designs:

- **MacBook Pro** — local Connection
  - **atc**
    - `workspace-ui` — Jujutsu workspace, active
    - `connection-reliability` — Git worktree, recent
  - **dotfiles** — no Workspaces
- **Studio** — remote Connection
  - **website**
    - `homepage-refresh` — Git worktree, active

Inside `workspace-ui`:

- Editor — Neovim, running
- Agent Sessions
  - `Plan dashboard` — Codex, running
  - `Review workspace model` — Claude Code, ended
- Terminals
  - `Tests` — running
  - `Server` — running
