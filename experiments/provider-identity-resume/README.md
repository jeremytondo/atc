# Provider identity and resume POC

This directory contains deliberately independent provider probes for
[ATC-68](https://linear.app/elevenideas/issue/ATC-68/poc-provider-identity-and-resume-with-codex-app-server-and-claude).
They are experiments, not ATC production session or adapter code.

The Codex checkpoints cover conversation creation, a second turn in the same
app-server process, verified resume from a fresh app-server process, and the
dormant zero-turn lifecycle. The Claude probe and the remaining test matrix
will follow after the current gate output is reviewed.

## Setup

Prerequisites:

- `mise`
- an installed and authenticated `codex` CLI with `app-server` support

From the repository root:

```sh
mise run -C experiments/provider-identity-resume setup
```

The probe inherits local Codex authentication. It does not inspect or print
environment variables, config files, or credentials.

## Codex create and same-process turns

```sh
cd experiments/provider-identity-resume
pnpm codex create --cwd ../..
```

The command:

1. starts `codex app-server --stdio`;
2. performs `initialize` and `initialized`;
3. creates a durable thread with a read-only sandbox and approvals disabled;
4. records exactly when the thread ID first appears;
5. runs a harmless marker turn;
6. verifies marker continuity in a second turn in the same process; and
7. writes an ignored session artifact and raw JSONL provider log under
   `runs/codex/`.

On success, it prints a copy-paste `pnpm codex resume --session ...` command.

## Codex fresh-process resume

Run the exact command printed by `create`, for example:

```sh
pnpm codex resume --session 'runs/codex/<run>.session.json'
```

The resume command starts a new app-server process and calls `thread/resume`.
Before sending a turn, it verifies that the returned thread ID and working
directory match the session artifact. It never falls back to `thread/start`, so
an invalid resume ID cannot silently create a replacement conversation. The
resumed turn then asks Codex to recall the marker without including the marker
in the prompt.

Expected final lines:

```text
IDENTITY VERIFIED: thread/resume returned id=<same id>
CWD VERIFIED: <same cwd>
CONTINUITY VERIFIED: resumed turn returned <same marker>
PASS: a fresh app-server process resumed the exact thread, cwd, and context.
```

## Codex dormant zero-turn lifecycle

This scenario creates a durable thread but deliberately does not call
`turn/start` during a configurable dormant interval. It then sends the
thread's first turn and verifies that the response is attributed to the
original thread.

```sh
cd experiments/provider-identity-resume
pnpm codex dormant --cwd ../.. --wait-seconds 30
```

For a longer manual check, increase `--wait-seconds`. The default is 30
seconds. On success, the command writes an ignored result artifact containing
the exact thread ID, cwd, requested and observed wait, timestamps, marker, and
raw-log path.

Expected final lines:

```text
DORMANT INTERVAL VERIFIED: observed <at-least-requested>ms without turn/start
CONTINUITY VERIFIED: first post-dormancy turn returned <marker>
PASS: thread <same id> accepted its first turn after remaining dormant for <observed>ms.
```

## Artifacts

`runs/` is ignored by source control.

- `*.jsonl` contains each app-server stdout line exactly as received. Client
  requests and formatted console messages are not mixed into this raw provider
  stream.
- `*.session.json` contains only the thread ID, marker, cwd, timestamp, and
  relative create-log path needed for the resume check.
- `*.dormant-zero-turn.json` records the dormant scenario's identity and
  timing evidence. Its sibling JSONL file remains the unmodified provider
  event stream.

Review a raw event stream with:

```sh
jq -c . runs/codex/<run>.create.jsonl
```

Generate the TypeScript schema for the locally installed Codex version with:

```sh
pnpm schema:codex
```

Generated schemas are ignored because the installed CLI is the authoritative
version for this POC.

## Safety and failure behavior

- Thread and turn requests use the read-only sandbox and approval policy
  `never`.
- Prompts explicitly prohibit tools, commands, and file changes.
- Any server-initiated approval or input request is rejected by the probe.
- Protocol errors, timeouts, identity mismatches, cwd mismatches, non-completed
  turns, missing marker output, and ephemeral threads all fail the command.
- Increase the five-minute turn timeout with `--timeout-seconds <n>` when
  diagnosing a slow provider response.

If `codex app-server` is unavailable, update the Codex CLI. If initialization
fails, first confirm that the native `codex` TUI is authenticated and working.
Provider diagnostics appear as `[app-server stderr]` lines; the JSONL artifact
contains protocol stdout only.

## Checks

```sh
mise run -C experiments/provider-identity-resume check
```
