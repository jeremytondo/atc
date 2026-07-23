# atc record deletion never changes files

> **Lifecycle amendment (2026-07):** Session Delete is stop-and-forget. A Live
> Session confirmed present is terminated before its record is removed; a Live
> Session confirmed absent or an Ended Session is removed directly, without
> first creating a tombstone. Inventory or termination failure preserves the
> record. Workspace deletion still uses the internal end operation for
> provisional or Live records before removing all associated metadata.

Deleting a Session stops it when necessary and removes its atc metadata, but never changes filesystem state. Deleting a Workspace stops all of its associated Sessions and removes their atc metadata only after every stop succeeds. Deleting a Project requires all of its Workspaces to have already been deleted. None of these operations removes a Workspace directory, Project directory, worktree, or any other filesystem state; if a Workspace Session stop fails, the Workspace and all metadata remain so the user can resolve the failure and retry.
