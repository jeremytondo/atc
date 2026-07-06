# AtelierCode

AtelierCode is a native client for working with Cockpit sessions on a remote workstation.

## Language

**Remote Workspace Root**:
A named folder on the Cockpit workstation that AtelierCode uses as a starting namespace for browsing and selecting session working directories. Directory symlinks reachable from a Remote Workspace Root remain inside the app's remote browsing domain, including their children, even when the symlink target resolves outside the root.
_Avoid_: Configured root, favorite, full-host browse, sandbox

**Highlighted Entry**:
The file or folder row currently targeted by pointer or keyboard navigation in a remote file browser.
_Avoid_: Selected row

**Chosen Folder**:
The current viewed remote directory that AtelierCode will use as a session working directory when the user confirms a folder picker.
_Avoid_: Selected file, selected row
