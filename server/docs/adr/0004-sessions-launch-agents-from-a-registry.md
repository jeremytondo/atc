# Sessions run typed Actions in explicit Environments

> **Terminology note (2026-07):** This ADR predates the atc rename. "atc" is now the atc server (`atc`), and `atc.toml` is now `atc.toml`.

Supersedes [ADR 0003](0003-sessions-run-arbitrary-commands.md).

ADR 0004 is intentionally edited in place as a living record for the launch
security boundary. This departs from the banner-only supersession style used for
ADR 0002/0003 by owner decision: the core rationale remains, but the primitive
names and launch environment model changed before release.

## Decision

`start` does not accept an arbitrary shell command. It starts either an
**Action** resolved against a server-side typed registry or the server-selected
Interactive Shell, with an optional **Environment** in a required Workspace.

An Action is what runs. It is a named command template with an executable
`command`, fixed `args`, optional initial prompt placement, optional typed
params, and an Action type: Action or Agent Action.

An Environment is how the Action runs. The default Environment is
`host-login-shell`, which wraps the Action argv as:

```text
[$SHELL_OR_/bin/sh, -l, -i, -c, shellJoin(inner)]
```

`Action-or-Interactive-Shell + Environment + workspaceId` is the start contract.
Action and Environment are independent registries. The Workspace resolves the
Project and working directory; callers cannot supply either. The Interactive
Shell is a server-selected terminal launch, not an Action supplied by the
caller.

An Agent Action does not create a separate Agent Session runtime. A
Session launched from one remains an ordinary Session with the same zmx
lifecycle, attach, input, and archive behavior as every other Session. Future
Agent integrations MAY report optional Agent Activity for that specific Session;
this activity is distinct from process lifecycle and is normalized by the
integration rather than inferred from terminal output.

## Rationale

ADR 0003 chose an arbitrary `command` string for generality and simplicity. The
security review of the MVP showed that choice puts arbitrary command execution
on the API surface. The API is served on the TCP listener as well as the
owner-only Unix socket, and the service is intended to gain remote reach.
"Any reachable caller runs any command" is the wrong default.

A command allowlist over an arbitrary-string API was rejected: matching a
re-parsed shell string is unsound, and it does not contain anything once
terminal input can inject arbitrary keystrokes into a running process. The
defensible boundary is transport-aware authentication plus removing raw command
text from start requests.

The typed Action registry keeps Action launch choices owner-controlled. Actions can be
managed by hand in `actions.json` or through the authenticated API, but session
start still accepts only a named Action plus closed enum/bool params. Free-form
string params are deliberately unsupported. `command` is the executable name or
path, not a shell command string; fixed command arguments belong in `args`.

The plain Interactive Shell is the only Action-free launch path. It is selected
by the server and accepts no caller-provided command, so it does not reopen the
arbitrary-command API boundary.

The Environment registry makes today's shell wrapper explicit instead of hiding
it inside zmx integration code. The Step 0 environment spike found:

- zmx sessions inherit the environment from the `zmx run` caller, which in
  normal operation is the atc service process.
- `$SHELL -l -i -c` is necessary and sufficient on this host to reconstruct the
  expected interactive PATH and mise setup from a stripped service environment.
- `$SHELL -l -c` is not enough on this host because important setup lives in
  interactive shell startup files.
- zmx provides a PTY, so `-i` does not hang detached `-c` launches.
- zmx honors `cmd.Dir`, so no explicit `cd` is needed in the shell wrapper.

Therefore `host-login-shell` preserves the behavior closest to "SSH in and run
it by hand", while leaving room for later container/mise/nix environments
without changing the multiplexer seam.

## Shape

- Config uses `actions.json` for API-managed Actions and `[environments]` in
  `atc.toml`.
- Built-in Actions are `claude` and `codex`, both prompt-capable commands.
- Built-in Actions are always present underneath the sparse `actions.json`
  overlay. File entries add custom Actions or override built-ins by name.
- Action type is chosen when an Action is created and cannot be changed. To
  reclassify an Action, create a new one.
- A custom Action cannot be deleted while it has active Sessions.
- Built-ins can be overridden, but not removed in v1. Deleting an override
  reverts to the built-in definition.
- The built-in `host-login-shell` Environment is additive; configured
  environments do not remove it.
- `GET /api/actions` exposes Action discovery with display metadata, params,
  prompt placement, Action type metadata, and origin.
- `GET /api/actions/{name}` exposes the full definition for edit UX.
- `POST`, `PUT`, and `DELETE /api/actions...` are normal authenticated API
  operations. The owner Unix socket is trusted; TCP uses the configured bearer
  token when one is set.
- `GET /api/environments` exposes selectable Environments.
- `POST /api/sessions/start` accepts required `workspaceId`, optional `action`,
  optional `environment`, `params`, optional `prompt`, and optional `name`.
  When `action` is omitted, atc starts the Interactive Shell; `params` and
  `prompt` then have no meaning and are rejected.
- Persisted Sessions store their required Workspace association, optional
  `action`, and `environment`.

## Consequences

- Unknown Action, unknown Environment, and invalid params are 400 caller errors
  validated before any zmx launch.
- The Action-free Interactive Shell is server-selected and never accepts a raw
  caller command.
- Invalid Action writes are 400 caller errors. A corrupt or invalid
  hand-edited `actions.json` is a 500 operator/config error on discovery or
  session start.
- Misconfigured Action or Environment is a 500 operator/config error.
- Multiplexer launch failure remains a 502 and returns the created `sessionId`.
- zmx integration accepts final argv and no longer owns shell literals or
  quoting.
- An Agent Action is an Action type, not a separate process or lifecycle
  primitive. Agent-specific behavior layers onto the generic Session model.
