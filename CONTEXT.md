# AtelierCode

AtelierCode is a native client for working with Cockpit sessions on a remote workstation.

## Language

**Connection**:
A named relationship from AtelierCode to one Cockpit server, whether that server is running locally or remotely. A Connection has its own identity apart from its display name and URL; its name is chosen in AtelierCode rather than discovered from Cockpit. Projects and Terminal Sessions belong to the Connection they come from, and matching project names on different Connections do not imply the same project.
_Avoid_: Workspace, account

**Project**:
A Cockpit-owned record for a named working area on one Cockpit server. AtelierCode displays Projects through their Connection, but does not own the Project record itself.
_Avoid_: Local project, app project

**Terminal Session**:
A Cockpit-owned terminal process created on a particular Cockpit server. AtelierCode displays and controls a Terminal Session through the Connection it came from, and its app UI is intentionally centered on project-scoped Terminal Sessions.
_Avoid_: Agent session

**Focused Sidebar Row**:
The Project or Terminal Session row currently targeted by keyboard navigation in the sidebar. A Focused Sidebar Row is distinct from the selected Terminal Session because Projects can be focused without becoming the active detail content.
_Avoid_: Highlighted Entry, selected row

**Remote Workspace Root**:
A named folder on the Cockpit workstation that AtelierCode uses as a starting namespace for browsing and selecting session working directories. Directory symlinks reachable from a Remote Workspace Root remain inside the app's remote browsing domain, including their children, even when the symlink target resolves outside the root.
_Avoid_: Configured root, favorite, full-host browse, sandbox

**Highlighted Entry**:
The file or folder row currently targeted by pointer or keyboard navigation in a remote file browser.
_Avoid_: Selected row

**Chosen Folder**:
The current viewed remote directory that AtelierCode will use as a session working directory when the user confirms a folder picker.
_Avoid_: Selected file, selected row

**Atelier Command Sequence**:
A keyboard sequence that starts with `Cmd-K`, waits for one unmodified next key, and targets AtelierCode itself, including when a Terminal Session has focus.
_Avoid_: Leader key, terminal prefix, command chord
