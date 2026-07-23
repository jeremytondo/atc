# atc

atc is a native client for working with atc Terminal Sessions on a remote workstation.

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

**Terminal Session**:
A Server-owned terminal process created on a particular atc server. A Terminal Session belongs to its Workspace, except for legacy Project-scoped sessions. Its lifecycle is Live or Ended; Ended is a retained read-only tombstone shown only after the server confirms the backing zmx session is absent. Transport and attach failures remain retryable and do not end it.
_Avoid_: Terminal, shell

**Agent Session**:
A Terminal Session configured to run a coding agent such as Codex or Claude Code. A Workspace may have multiple Agent Sessions.
_Avoid_: Agent
A Workspace Session that opens the server's default Interactive Shell or runs an Action from the Terminal creation flow. The macOS sidebar labels this group Terminals; the server treats it as a generic Session.
_Avoid_: Shell, generic Session

**Agent Session**:
A Workspace Session started from an Agent Action, such as Codex or Claude Code. The macOS sidebar labels this group Sessions; it remains a generic server Session and may later report Agent Activity.
_Avoid_: Agent

**Focused Sidebar Row**:
The Workspace, Agent Session, or Terminal Session row currently targeted by keyboard navigation in the sidebar. A Focused Sidebar Row is distinct from the selected Session because Workspaces can be focused without becoming the active detail content.
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
A two-step atc interaction that starts with the configured leader (`Cmd-K` by default), waits for one continuation key (modified or unmodified), and targets atc itself, including when a Terminal Session has focus. A Command Sequence is not a Keyboard Shortcut.
_Avoid_: Keyboard Shortcut, terminal prefix, command chord
