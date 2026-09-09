# Glossary

* **Domain** — one of the areas ATC models: Projects, Terminals, Threads. Each has resources and capabilities.
* **Capability** — an operation within a domain that an Integration can support.
* **Provider** — an external system that owns the real thing behind an ATC resource. zmx for terminal sessions; Claude Code, Codex, and T3 Code for conversations.
* **Integration** — ATC's built-in relationship with one external system, connecting it to the domains it serves or uses.
* **App** — a surface a person opens to work with a provider. Some run inside an ATC terminal, like the Claude Code and Codex CLIs. Some ATC only links to, like T3 Code desktop and web.
* **Agent** — the coding agent doing the work inside a Thread, as its Provider reports it. Claude Code and Codex are Agents. Their CLIs are Apps, and T3 Code is a Provider that runs them. ATC recognizes agents; most things you do with one happen through Threads.
* **Project** — a codebase with a root directory that gives shared context to Threads and other domains.
* **Terminal** — a persistent terminal session managed through ATC.
* **Space** — a group of Terminals with a default directory. Part of the Terminals domain.
* **Thread** — one conversation with an Agent, owned by a Provider, tracked and, where supported, started by ATC.
* **Turn** — one execution on a Thread, from a prompt to the agent's final response. Part of the Threads domain.
* **Client** — anything outside ATC that uses the API.
* **API** — the contract through which Clients reach ATC's domains and capabilities.
* **Artifact** — a published document: mutable metadata (title, Project) over a history of immutable Versions, read at one stable link that always shows the newest Version. Part of the Artifacts domain.
* **Version** — one publication of an Artifact: a complete static build, the authoring source it came from, and its provenance, at a permanent link. Versions are appended, never changed; restoring an old one publishes it again as the newest.
* **Working Copy** — an agent's local, editable project for one document on the shared authoring platform, kept outside project repositories until discarded. Once published, or started from a published Version, it records the Artifact and Version it revises; it is not a server-side draft.
* **Document origin** — the unprivileged listener, on its own port and browser origin, that serves published Artifacts to readers without credentials. Distinct from the API.
