# atc

atc is a native client for working with atc Sessions on a remote workstation.

## Language

**Connection**:
A named relationship from atc to one atc server, whether that server is running locally or remotely. A Connection has its own identity apart from its display name and URL; its name is chosen in atc rather than discovered from the server. Projects, Workspaces, and Sessions belong to the Connection they come from, and matching project names on different Connections do not imply the same project.
_Avoid_: Account

**Dashboard**:
The launch and Project-level surface for finding, creating, and opening Workspaces. The initial version has no separate Project detail screen, Recent Workspaces section, or activity feed.
_Avoid_: Project detail, home sidebar

**Project**:
A Server-owned record for one codebase on an atc server. A Project provides the default repository folder for its Workspaces; atc displays Projects through their Connection, but does not own the Project record itself.
A Project can be deleted only after its Workspaces are deleted. Deletion does not change filesystem state.
_Avoid_: Local project, app project

**Workspace**:
A Server-owned task context within one Project. In the initial version, every Workspace uses its Project's default folder, owns its Sessions, and persists after they end. Its name is user-owned, renameable, and need not be unique. Deleting a Workspace never changes its working directory or other filesystem state, but stops and deletes all of its associated Sessions only after every stop succeeds.
_Avoid_: Checkout, worktree

**Workspace Startup Configuration**:
The local macOS preference that lists Action and Interactive Shell entries for Workspace startup. Each Connection has defaults; a Project either uses those defaults with live inheritance or has a Custom configuration copied once and then independent. An explicitly empty Custom configuration suppresses the Connection defaults.
_Avoid_: Workspace template, server startup configuration

**Default Session**:
The one entry designated as the Default in every nonempty Workspace Startup Configuration. The first entry added becomes Default, the designation can be transferred, and removing it promotes the earliest remaining entry.
_Avoid_: Primary Session, first Session

**Session**:
A Server-owned terminal process created on a particular atc server. A Session belongs to its Workspace, except for legacy Project-scoped Sessions. Its lifecycle is Live or Ended; Ended is a retained read-only tombstone shown only after the server confirms the backing zmx session is absent. Transport and attach failures remain retryable and do not end it.
_Avoid_: Terminal Session, shell

**Session Identity**:
The macOS user-facing identity copied onto a Session at launch: its Agent or Action name, or `Shell` for the Interactive Shell. An indexed Session presents as `[index] Identity`, followed by ` · Custom Name` when set; the optional Custom Name supplements and never replaces the identity.
_Avoid_: Session name, Terminal

**Session Index**:
An immutable positive Workspace-local address allocated by the server in one namespace shared by Sessions and Terminals. The Workspace Navigator and Session picker sort each group by ascending Session Index; gaps are expected, and legacy index-less Sessions sort last.
_Avoid_: Session ID, row number

**Terminal**:
The macOS category for a Workspace Session that opens the server's default Interactive Shell or runs a non-Agent Action from the Terminal creation flow. It appears in the Terminals group, but its Session Identity is `Shell` or the Action name, never `Terminal`.
_Avoid_: Terminal Session, Shell Session

**Agent Session**:
A Workspace Session started from an Agent Action, such as Codex or Claude Code. The macOS sidebar labels this group Sessions; it remains a generic server Session and may later report Agent Activity.
_Avoid_: Agent

**Focused Sidebar Row**:
The Workspace, Agent Session, or Terminal row currently targeted by keyboard navigation in the sidebar. A Focused Sidebar Row is distinct from the selected Session because Workspaces can be focused without becoming the active detail content.
_Avoid_: Highlighted Entry, selected row

**Remote Workspace Root**:
A named folder on the server workstation that atc uses as a starting namespace for browsing and selecting session working directories. Directory symlinks reachable from a Remote Workspace Root remain inside the app's remote browsing domain, including their children, even when the symlink target resolves outside the root.
_Avoid_: Configured root, favorite, full-host browse, sandbox

**Highlighted Entry**:
The file or folder row currently targeted by pointer or keyboard navigation in a remote file browser.
_Avoid_: Selected row

**Chosen Folder**:
The current viewed remote directory that atc will use as a session working directory when the user confirms a folder picker.
_Avoid_: Selected file, selected row

**Command Sequence**:
A two-step atc interaction that starts with the configured leader (`Cmd-K` by default), waits for one continuation key (modified or unmodified), and targets atc itself, including when a Session has focus. A Command Sequence is not a Keyboard Shortcut.
_Avoid_: Keyboard Shortcut, terminal prefix, command chord
