# Local Connections scope atc Projects

> **Terminology note (2026-07):** This ADR predates the atc rename. "atc" is now the atc server (`atc`).

atc represents each atc server as a local Connection with an app-chosen name, URL, token, and stable local identity, while Projects and Terminal Sessions remain atc-owned records. The app scopes displayed Projects and Terminal Sessions by the Connection they came from instead of trying to make atc project or session IDs globally meaningful across servers; this lets multiple local or remote atc servers appear together without adding cross-server identity to the atc API.
