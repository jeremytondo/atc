# atc record deletion never changes files

> **Lifecycle amendment (2026-07):** “Stops” below means the internal end
> operation for provisional or Live records. Ended Sessions need no zmx action,
> and Session Delete is the only public Session lifecycle operation.

Deleting a Session stops it when necessary and removes its atc metadata, but never changes filesystem state. Deleting a Workspace stops all of its associated Sessions and removes their atc metadata only after every stop succeeds. Deleting a Project requires all of its Workspaces to have already been deleted. None of these operations removes a Workspace directory, Project directory, worktree, or any other filesystem state; if a Workspace Session stop fails, the Workspace and all metadata remain so the user can resolve the failure and retry.
