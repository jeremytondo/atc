# Drive agents through their terminal, not ACP

> **Terminology note (2026-07):** This ADR predates the Atelier Code rename. "Cockpit" is now the Atelier Code server (`atc`).

`docs/archive/idea.md` originally proposed handing work to AI coding agents over ACP
(Agent Client Protocol). We are instead orchestrating agents by spawning their
real native CLI (`claude`, `codex`) in a persistent terminal session and driving
it by injecting keystrokes, as if a human were typing.

## Why

- **Subscription compatibility.** ACP does not work with Claude Code
  subscription pricing; driving the real `claude` binary does.
- **Universality.** Any terminal/TUI agent can be driven the same way, with no
  per-agent protocol integration.
- **Looser coupling.** Cockpit depends on a narrow multiplexer abstraction
  (start / send text / send key / list), not on an agent's protocol surface.

## Consequences

- We give up ACP's structured, machine-readable I/O. Reading agent output back
  programmatically becomes a separate problem (terminal scrollback / streaming)
  rather than a protocol feature.
- ACP is not ruled out forever; it could return as an alternative driver behind
  the same orchestration concept. This ADR records that terminal-driving is the
  chosen MVP path, superseding the idea-doc plan.
