# Sessions run an arbitrary command, not a fixed agent enum

> **Superseded by [ADR 0004](0004-sessions-launch-agents-from-a-registry.md).**
> A security review of the MVP reversed this decision: `start` now launches an
> agent from a server-side registry rather than an arbitrary command string.
> The reasoning below is retained for history.

DEV-22 is framed entirely around AI coding agents (`claude`/`codex`). We are
instead building the more general primitive it implies: `start` runs an
**arbitrary command** in a persistent, scoped Session. Agents are the first use
case, not a constrained field.

## Why

- DEV-22's own opening states the real goal: "Cockpit needs to **perform actions
  on the workstation** … the **first such action** is an AI coding session." The
  general capability is the point; the agent is example #1.
- It is simpler, not more complex: there is no agent enum and no
  agent-validation branch. A command is just a non-empty string.
- It is more useful immediately (`npm run dev`, `htop`, a REPL) and keeps the
  door open for non-agent workstation automation.

## Shape

- `start` takes a `command` (shell string), a `dir`, and an optional scope. No
  `--agent` flag. The command is launched via `$SHELL -l -i -c "<command>"` so
  it inherits the full environment.
- The session name encodes *scope* (`cockpit:item:DEV-22`), never the command.
  What is running is read from `zmx list` (`cmd=…`) when needed.

## Consequences

- No pre-flight that the command exists (fire-and-forget). A bad command yields a
  session whose shell prints "command not found", visible on attach. This is
  consistent with output-reading being out of scope for the MVP.
- "Agent" remains a domain concept (a kind of command), but is not a Session
  field. Agent-specific affordances, if needed later, are an additive layer.
