# atc server

atc is an ideation, planning, workflow, and AI-coding-orchestration platform.
A local Go service owns the domain; CLI, web, and (future) iOS clients use it
through an API boundary. This glossary fixes the language the codebase and docs
use for these concepts.

## Language

### Orchestration

**Session**:
A persistent terminal created from an Action, an Environment, and a working
directory, then living independently of the atc service. atc starts it,
injects input, and can re-attach to it later; it does not own its foreground
process. Its atc identity is independent of its multiplexer handle. Chosen
over "Run"/"Agent Run" deliberately, despite "session" being the underlying zmx
term.
_Avoid_: terminal, tab, pane, job, task, run, agent run.

**Action**:
The named command template a Session runs. Actions are operator-defined config
with a required executable `command`, fixed `args`, optional initial prompt
placement, and optional typed params.
_Avoid_: raw command, arbitrary command, harness.

**Agent**:
An AI coding tool (currently `claude` or `codex`) used through its real native
CLI/TUI rather than a protocol. Agent is product language for some Actions, not
the backend primitive and not a required property of every Session.
_Avoid_: harness, bot, model, assistant.

**Environment**:
The named launch wrapper that decides how/where an Action runs. The default is
`host-login-shell`, which runs the Action argv through the host user's
login-interactive shell. Future Environments may wrap Actions with containers,
mise, nix, or other launch contexts.
_Avoid_: shell seam, runtime, executor.

**Session Input**:
Text or named keys injected into a Session's terminal as if a human typed them.
Used instead of speaking a structured protocol to the Agent.
_Avoid_: drive, control, puppet, automate.

**Multiplexer**:
The external tool that owns Session process lifecycle and PTY persistence
(currently `zmx`). atc treats it as a swappable dependency behind a narrow
internal abstraction (start argv / send text / send key / attach / list).
_Avoid_: terminal server, pty manager, tmux.

### Projects

**Project**:
An atc-owned record that names one workstation directory and groups related
Sessions around that directory.
_Avoid_: workspace, repository, folder, app project.

**Project Working Directory**:
The absolute workstation directory a Project names and uses as the default
working directory for Project Sessions.
_Avoid_: workspace root, filesystem root, project root.
