# atc Product Context

Canonical product language for the atc app and its user-facing domain.

## Language

**Active Workspace**:
The Workspace currently scoping workspace-specific navigation and commands in a window. It remains active while global content is shown and may be disconnected or archived; it ceases to be active only when cleared or removed.
_Avoid_: Open Workspace, Available Workspace

**Navigator**:
A window-level sidebar mode that determines which navigation collection the sidebar presents. Its selection remains stable when the Active Workspace changes and does not select main content by itself.
_Avoid_: Sidebar tab, Workspace navigator state

**Projects Navigator**:
The app-wide Navigator containing the Dashboard and multiple unarchived Projects with their unarchived nested Workspaces. The plural name reflects that its scope crosses Project boundaries; archived records are managed from the Dashboard instead of this Navigator.
_Avoid_: Global Navigator, Home Navigator, Project Navigator

**Workspace Navigator**:
The Navigator containing the Sessions and Terminals of the single Active Workspace.
_Avoid_: Workspaces Navigator, Sessions Navigator

**File Navigator**:
The Navigator containing the file tree of the single Active Workspace.
_Avoid_: Files Navigator, File browser Navigator

**Workspace Switcher**:
The toolbar context pill whose Workspace region identifies the Active Workspace as `Project › Workspace` and opens the app-wide Workspace selection menu. When a Session is selected, a second region shows `› [index] Identity` with an optional ` · Custom Name` and opens the Active Workspace's Session picker, grouped into Sessions and Terminals.
_Avoid_: Workspace Picker, Project picker, Breadcrumb

**Command Palette**:
The keyboard-driven surface for finding and invoking commands or navigating to live targets from anywhere in a window.
_Avoid_: Workspace Picker, Workspace search

**Keyboard Shortcut**:
A one-step key binding that directly invokes an atc command, such as `Cmd-B`.
_Avoid_: Command Sequence, key chord

**Command Sequence**:
A two-step atc interaction that begins with the configured leader and invokes a command after a continuation key, such as `Cmd-K, B`. A Command Sequence is not a Keyboard Shortcut.
_Avoid_: Keyboard Shortcut, key chord, terminal prefix

**Dashboard**:
The app-wide main-content destination for viewing and managing work across Connections, Projects, and Workspaces. Showing the Dashboard does not clear the Active Workspace or change the selected Navigator.
_Avoid_: Overview, Dashboard mode, Dashboard route
