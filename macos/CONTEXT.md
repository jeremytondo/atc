# atc

atc is a native client for working with atc Terminal Sessions on a remote workstation.

## Language

**Connection**:
A named relationship from atc to one atc server, whether that server is running locally or remotely. A Connection has its own identity apart from its display name and URL; its name is chosen in atc rather than discovered from the server. Projects and Terminal Sessions belong to the Connection they come from, and matching project names on different Connections do not imply the same project.
_Avoid_: Workspace, account

**Project**:
A Server-owned record for one codebase on an atc server. A Project provides the default repository folder for its Workspaces; atc displays Projects through their Connection, but does not own the Project record itself.
_Avoid_: Local project, app project

**Workspace**:
A Server-owned task context within one Project. In the initial version, every Workspace uses its Project's default folder while owning its own Agent Sessions and Terminals.
_Avoid_: Checkout, worktree

**Terminal Session**:
A Server-owned terminal process created on a particular atc server. A Terminal Session belongs to its Workspace, except for legacy Project-scoped sessions.
_Avoid_: Terminal, shell

**Agent Session**:
A Terminal Session configured to run a coding agent such as Codex or Claude Code. A Workspace may have multiple Agent Sessions.
_Avoid_: Agent

**Focused Sidebar Row**:
The Project or Terminal Session row currently targeted by keyboard navigation in the sidebar. A Focused Sidebar Row is distinct from the selected Terminal Session because Projects can be focused without becoming the active detail content.
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

**Atelier Command Sequence**:
A keyboard sequence that starts with `Cmd-K`, waits for one unmodified next key, and targets atc itself, including when a Terminal Session has focus.
_Avoid_: Leader key, terminal prefix, command chord
