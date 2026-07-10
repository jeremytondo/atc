# Remote file browsing starts from Remote Workspace Roots

> **Terminology note (2026-07):** This ADR predates the Atelier Code rename. "Cockpit" is now the Atelier Code server (`atc`).

AtelierCode remote file browsing starts from static Cockpit-configured Remote Workspace Roots, with Cockpit synthesizing a `Home` root at the server user's `$HOME` when none are configured. This gives the app a clear set of remote browsing starting points without making the first file browser a full-host explorer or a root-management UI. Remote Workspace Roots are a browsing namespace, not a resolved-path sandbox: symlinked directories reached from a root remain browseable, and the boundary does not yet globally restrict `POST /api/sessions/start`.
