# atc

atc is a native client for driving durable agent Threads and Terminals on an
atc App Server.

## Language

**Connection**:
A named relationship from atc to one App Server, local or remote. A Connection
has its own identity apart from its display name and URL; its name is chosen
in atc rather than discovered from the server. Projects, Threads, and
Terminals belong to the Connection they come from, and matching project names
on different Connections do not imply the same project.
_Avoid_: Account

**Project**:
A server-owned record for one codebase, carrying the default working directory
for new Threads and Terminals. atc displays Projects through their Connection
but does not own the record. Deleting a Project deletes every Thread and
Terminal it owns; deletion never changes filesystem state.
_Avoid_: Local project, app project, Workspace

**Thread**:
A durable agent conversation (Codex or Claude Code) — the primary object users
create and navigate. A Thread belongs to a Project, has an Agent and an
immutable working directory, and is always reopenable in either of its two
views, Chat or TUI. Threads have no Ended state; a Thread whose TUI terminal
exited simply relaunches when opened in TUI.
_Avoid_: Session, conversation

**Chat**:
One of the two views of a Thread, and the default: the Thread's transcript
and a composer, driven natively through the App Server with no Terminal
involved. Chat and TUI show the same conversation; which one a Thread is
shown in is remembered for the app session and shared by every window. The
App Server owns a Thread by default: switching a Claude Code Thread to Chat
hands it back (its TUI ends once the current turn is on disk), and a Chat
prompt sent while its TUI is live takes over the same way — the TUI comes
back when the prompt is done. Codex Threads need no hand-off.
_Avoid_: Native mode, transcript view

**TUI**:
The other view of a Thread: its provider TUI running in the linked Terminal.
Opening a Thread in TUI launches or reuses that Terminal; while the App
Server is driving a turn on a Claude Code Thread, the TUI opens once that
turn ends.
_Avoid_: Terminal mode, terminal view

**Terminal**:
A server-owned zmx-backed process. A Thread's TUI runs in a linked Terminal
reached only through its Thread; standalone Terminals are Project-level tools
(shells, editors, watchers) listed in the sidebar's Terminals section. Its
lifecycle is Live or Ended; Ended is a retained read-only tombstone shown only
after the server confirms the backing zmx session is absent. Transport and
attach failures are retryable and never end it.
_Avoid_: Terminal Session, shell

**Agent**:
A built-in provider (Codex, Claude Code) with live-detected availability and
an actionable reason when unavailable.
_Avoid_: Model, assistant

**Activity State**:
The server's normalized view of what a Thread's agent is doing: idle, working,
needs input, or unknown. Needs input is the one state the UI must surface;
idle and unknown are unmarked.
_Avoid_: Status, health

**Pinned Thread**:
A server-backed shortcut (`pinnedAt`) shared by every client. Pins order by
pin time, oldest first, sit above the filter, and are excluded from the recent
list. Archiving clears a pin.
_Avoid_: Favorite, bookmark

**Archived Thread**:
An organizational state that removes a Thread from normal navigation without
deleting it. Archived Threads are managed (restored or deleted) from the
sidebar's Archived filter; restoring reopens the Thread.
_Avoid_: Deleted, closed

**Dashboard**:
The launch surface: one card per Project, grouped by Connection. Navigating to
Dashboard never clears the launch-local Project context.
_Avoid_: Home, project detail

**Project context**:
The launch-local active Project, established by opening or creating a Thread.
It scopes the sidebar's Terminals section and New Thread defaults, and resets
on every app launch.
_Avoid_: Selection, current project

**Command Sequence**:
A two-step atc interaction that starts with the configured leader (`Cmd-K` by
default), waits for one continuation key (modified or unmodified), and targets
atc itself, including when a Terminal has focus. A Command Sequence is not a
Keyboard Shortcut.
_Avoid_: Keyboard Shortcut, terminal prefix, command chord
