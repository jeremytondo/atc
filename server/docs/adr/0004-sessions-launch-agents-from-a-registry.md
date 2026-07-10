# Sessions run typed Actions in explicit Environments

> **Terminology note (2026-07):** This ADR predates the atc rename. "atc" is now the atc server (`atc`), and `atc.toml` is now `atc.toml`.

Supersedes [ADR 0003](0003-sessions-run-arbitrary-commands.md).

ADR 0004 is intentionally edited in place as a living record for the launch
security boundary. This departs from the banner-only supersession style used for
ADR 0002/0003 by owner decision: the core rationale remains, but the primitive
names and launch environment model changed before release.

## Decision

`start` does not accept an arbitrary shell command. It takes an **Action** name
resolved against a server-side typed registry, optional typed params, an optional
**Environment** name, and a working directory.

An Action is what runs. It is a named command template with an executable
`command`, fixed `args`, optional initial prompt placement, and optional typed
params.

An Environment is how the Action runs. The default Environment is
`host-login-shell`, which wraps the Action argv as:

```text
[$SHELL_OR_/bin/sh, -l, -i, -c, shellJoin(inner)]
```

`Action + Environment + workingDir` is the start contract. Action and
Environment are independent registries.

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

The typed Action registry keeps launch choices owner-controlled. Actions can be
managed by hand in `actions.json` or through the authenticated API, but session
start still accepts only a named Action plus closed enum/bool params. Free-form
string params are deliberately unsupported. `command` is the executable name or
path, not a shell command string; fixed command arguments belong in `args`.

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
- Built-ins can be overridden, but not removed in v1. Deleting an override
  reverts to the built-in definition.
- The built-in `host-login-shell` Environment is additive; configured
  environments do not remove it.
- `GET /api/actions` exposes Action discovery with display metadata, params,
  prompt placement, and origin.
- `GET /api/actions/{name}` exposes the full definition for edit UX.
- `POST`, `PUT`, and `DELETE /api/actions...` are normal authenticated API
  operations. The owner Unix socket is trusted; TCP uses the configured bearer
  token when one is set.
- `GET /api/environments` exposes selectable Environments.
- `POST /api/sessions/start` accepts `action`, optional `environment`, `params`,
  `workingDir`, optional `prompt`, and optional `name`.
- Persisted Sessions store `action` and `environment`.

## Consequences

- Unknown Action, unknown Environment, and invalid params are 400 caller errors
  validated before any zmx launch.
- Invalid Action writes are 400 caller errors. A corrupt or invalid
  hand-edited `actions.json` is a 500 operator/config error on discovery or
  session start.
- Misconfigured Action or Environment is a 500 operator/config error.
- Multiplexer launch failure remains a 502 and returns the created `sessionId`.
- zmx integration accepts final argv and no longer owns shell literals or
  quoting.
- "Agent" remains useful product language for some commands, but it is not the
  backend primitive.
