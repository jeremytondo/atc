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
* **Thread** — one conversation with an Agent, owned by a Provider, tracked and, where supported, driven by ATC.
* **Turn** — one execution on a Thread, from a prompt to the agent's final response. Part of the Threads domain.
* **Message** — one text a Client sends into an existing Thread through ATC; it starts the next Turn or directs the one running, and ATC keeps its identity so a lost answer is recovered rather than sent twice. Part of the Threads domain.
* **Approval request** — a request for approval an Agent is blocked on inside a Thread, as its Provider reports it; ATC presents it with the decisions it offers and relays exactly one. Part of the Threads domain.
* **Input request** — a set of structured questions an Agent is blocked on inside a Thread, as its Provider reports it; ATC presents each question with the choices and answer forms it allows, relays one complete answer set, and reports the request resolved only on the Provider's evidence. Part of the Threads domain.
* **Stop** — an explicit operation that ends a Thread's work — the Turn running and any Turn already submitted — while keeping the conversation for a later Message; ATC records it before anything reaches the Provider, refuses new work until the Provider's evidence resolves it, and never calls it done on the command's acceptance alone. Part of the Threads domain.
* **Client** — anything outside ATC that uses the API.
* **API** — the contract through which Clients reach ATC's domains and capabilities.
