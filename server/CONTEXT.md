# atc server

atc is an ideation, planning, workflow, and AI-coding-orchestration platform.
A local Go service owns the domain; CLI, web, and (future) iOS clients use it
through an API boundary. This glossary fixes the language the codebase and docs
use for these concepts.

## Language

### Orchestration

**Session**:
A persistent wrapper around a zmx session, created from an Action or the
server-selected Interactive Shell in a Workspace working directory, then living
independently of the atc service. Its public lifecycle is Live or Ended; the
provisional Launch Attempt used during startup is not a Session. atc starts it,
injects input, and can re-attach while it is Live. Its atc identity is
independent of its multiplexer handle. Chosen
over "Run"/"Agent Run" deliberately, despite "session" being the underlying zmx
term. Delete ends a Live process when necessary and removes its atc record;
there is no Session archive lifecycle.
_Avoid_: terminal, tab, pane, job, task, run, agent run.

**Action**:
The named command template a Session runs. Actions are operator-defined config
with a required executable `command`, fixed `args`, optional initial prompt
placement, and optional typed params. An Action is either a general Action or
an Agent Action; its type is fixed when it is created. A custom Action cannot
be deleted while it has active Sessions.
_Avoid_: raw command, arbitrary command, harness.

**Launch Attempt**:
The internal provisional record created immediately before zmx launch. It
protects launch/deletion races and is either promoted to a Live Session or
deleted. It is never returned by Session APIs.
_Avoid_: starting Session, pending Session.

**Agent Action**:
An Action typed to launch an Agent. It may later declare Agent integration
metadata, but it uses the same command and Session lifecycle as every Action.
_Avoid_: non-Agent Action, agent command

**Agent**:
An AI coding tool (currently `claude` or `codex`) used through its real native
CLI/TUI rather than a protocol. An Agent is represented by an Agent Action, not
by a separate process or lifecycle domain.
_Avoid_: harness, bot, model, assistant.

**Agent Activity**:
An optional state reported by an Agent integration for one Session, such as
working, needs input, or completed. It is distinct from a Session's process
lifecycle and does not apply to every Session.
_Avoid_: Session status, process status

**Interactive Shell**:
The server-selected shell started when a Session has no Action. It provides the
plain terminal prompt without accepting an arbitrary caller-provided command.
_Avoid_: shell action, raw command

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
An atc-owned record that names one workstation directory and contains related
Workspaces. Workspace Sessions reach their Project through their Workspace. A
Project can be archived only after all of its Workspaces are archived.
It can be deleted only after all of its Workspaces are deleted, without changing
filesystem state.
_Avoid_: workspace, repository, folder, app project.

**Workspace**:
An atc-owned task context within one Project. In the initial version, a
Workspace uses the Project Working Directory, owns its Sessions, persists after
they end, and may be archived only when it has no active Sessions. Its name is
user-owned, renameable, and need not be unique. Deleting a Workspace never
changes its working directory or other filesystem state, but ends and deletes
all of its associated Sessions only after every end succeeds. Archive is
reversible through unarchive.
_Avoid_: checkout, worktree, disposable session group

**Project Working Directory**:
The absolute workstation directory a Project names and uses as the default
working directory for Project Sessions.
_Avoid_: workspace root, filesystem root, project root.
