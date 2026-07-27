# Provider identity and resume POC

This directory contains deliberately independent provider probes for
[ATC-68](https://linear.app/elevenideas/issue/ATC-68/poc-provider-identity-and-resume-with-codex-app-server-and-claude).
They are experiments, not ATC production session or adapter code.

The Codex checkpoints cover conversation creation, same-process turns,
fresh-process resume, dormant and restarted zero-turn behavior, invalid resume
safety, shared-process multiplexing, and native-TUI interoperability. The
Claude checkpoints cover session creation, a second same-process SDK query,
fresh-process exact-session resume, invalid or missing resume safety with
explicit replacement-session detection, and bounded streaming lifecycle
evidence, including blocking `AskUserQuestion` and harmless Bash permission
requests.

## Setup

Prerequisites:

- `mise`
- an installed and authenticated `codex` CLI with `app-server` support
- an authenticated Claude Code installation whose local credentials the Claude
  Agent SDK can inherit

From the repository root:

```sh
mise run -C experiments/provider-identity-resume setup
```

The probes inherit local provider authentication. They do not inspect or print
environment variables, config files, or credentials.

## Claude create and same-process resume

```sh
cd experiments/provider-identity-resume
pnpm claude create --cwd ../..
```

The command uses the Claude Agent SDK directly. It runs a harmless marker turn,
captures the durable `session_id` at its first appearance, then starts a second
SDK query in the same Node.js process with that exact ID in the SDK's `resume`
option. The second prompt does not contain the marker.

The probe disables filesystem settings and all built-in tools, selects
`dontAsk`, and validates those values from Claude's `system/init` message. It
also requires every SDK message carrying a session ID to use the expected ID.

On success, the command prints a copy-paste
`pnpm claude resume --session ...` command.

## Claude fresh-process resume

Run the exact command printed by `create`, for example:

```sh
pnpm claude resume --session 'runs/claude/<run>.session.json'
```

This separate command is the fresh client process. Before accepting the turn,
it requires the resumed `system/init` and result to report the recorded session
ID and cwd. It then verifies continuity without including the marker in the
prompt. There is no fallback that creates a new session.

Expected final lines:

```text
IDENTITY VERIFIED: resumed system/init and result returned session_id=<same id>
CWD VERIFIED: <same cwd>
CONTINUITY VERIFIED: resumed turn returned <same marker>
PASS: a fresh SDK client process resumed the exact Claude session, cwd, and context.
```

## Claude invalid-resume safety

```sh
pnpm claude invalid-resume --cwd ../..
```

The probe inventories programmatic Claude sessions for the exact working
directory, attempts to resume a well-formed nonexistent UUID, and inventories
the sessions again. It fails if the SDK emits a different session ID or if any
new session appears, including a newly created session using the requested
invalid UUID.

Omitting `resume` normally tells the SDK to create a session, so the missing-ID
case is enforced at the probe boundary: it must fail before `query()` starts.
The result artifact records both errors, every emitted session ID, the
before/after inventories, and the relative raw JSONL path.

## Claude lifecycle signals

```sh
pnpm claude lifecycle --cwd ../..
```

This command uses SDK streaming input and keeps the input side open for two
seconds after the successful result. The bounded window tests whether Claude
emits explicit `system/session_state_changed` messages while a turn runs or
settles without leaving the probe hung when no state event arrives. Configure
the window with `--observation-seconds <n>`.

The structured result records:

- the first provider activity message after `system/init`;
- the successful result boundary;
- every explicit `running`, `idle`, or `requires_action` transition;
- the capabilities reported by `system/init`; and
- an observed/not-observed summary for ATC's `working`, `idle`, and
  `needs_input` candidates.

The probe enables Anthropic's required
`CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1` subprocess setting while preserving
the inherited environment. Observed on Agent SDK `0.3.220` / Claude Code
`2.1.220`: `running` arrived before `system/init`, and authoritative `idle`
arrived after `result/success`. The following input-request round tests
`requires_action` separately.

## Claude input request

```sh
pnpm claude input-request --cwd ../..
```

The probe exposes only `AskUserQuestion`, prints the provider-generated
question, and waits for a terminal answer. It records the unmodified SDK
message stream plus a separate derived control timeline that correlates the
`canUseTool` request and response by request and tool-use ID.

For a repeatable non-interactive run:

```sh
pnpm claude input-request --cwd ../.. --answer Alpha
```

The deterministic path holds the callback for two seconds by default so the
probe can require `requires_action` while the callback is pending. Configure
that window with `--response-delay-seconds <n>`. The scenario fails unless the
same session resumes after the answer and reaches authoritative `idle`. No
filesystem, shell, network, or settings tool is exposed.

## Claude permission request

```sh
pnpm claude permission-request --cwd ../..
```

This command exposes only `Bash`, installs an inline `Bash(pwd)` ask rule, and
refuses every callback whose tool name or command differs from exact
`Bash`/`pwd`. The terminal prompt can allow or deny the request. For a
repeatable approval run:

```sh
pnpm claude permission-request --cwd ../.. --decision allow
```

The probe requires the permission callback to overlap the provider's
`requires_action` interval, correlates the response by request and tool-use ID,
and verifies that an allowed command returns the exact requested cwd before
the same session becomes authoritative `idle`. The raw SDK messages and
derived callback/provider timeline are retained separately.

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

## Codex zero-turn recovery

This scenario starts a durable thread without a turn, stops app-server, starts
a fresh app-server, and attempts to resume the exact ID before sending the
first turn:

```sh
pnpm codex zero-turn-recovery --cwd ../..
```

The command writes separate raw logs for the create and resume processes. It
also writes a structured result artifact on either pass or resume failure.

Observed on `codex-cli 0.145.0`: **fail**. `thread/start` returns a durable ID,
cwd, and empty turns, but the fresh process rejects `thread/resume` with
`-32600 no rollout found for thread id <same id>`. No first turn is attempted.

## Codex invalid-resume safety

```sh
pnpm codex invalid-resume --cwd ../..
```

The probe records every thread ID returned by `thread/list`, attempts both a
well-formed nonexistent ID and a request with no `threadId`, then lists again.
It requires clear errors and fails if any replacement ID appears.

## Codex shared-process multiplexing

```sh
pnpm codex multiplex --cwd ../..
```

The probe creates two durable threads in one app-server, requires both IDs in
`thread/loaded/list`, then runs A1 → B1 → A2 → B2. Each thread gets a distinct
marker. Every turn and item event is correlated by `turnId` and must carry the
expected `threadId`; unrelated lifecycle events for the other loaded thread
remain visible in the raw stream.

## Codex native-TUI round trip

This command requires an interactive terminal:

```sh
pnpm codex tui-round-trip --cwd ../..
```

It creates and seeds a thread through app-server, stops app-server, opens that
exact ID with `codex resume`, and supplies a harmless native-TUI marker prompt.
After the marker appears, exit the TUI with `/exit`. A fresh app-server then:

1. resumes the exact ID and cwd;
2. finds the distinct TUI marker in the returned turn history; and
3. completes another turn that recalls the TUI marker without including it in
   the prompt.

## Artifacts

`runs/` is ignored by source control.

- `*.jsonl` contains each app-server stdout line exactly as received. Client
  requests and formatted console messages are not mixed into this raw provider
  stream.
- `runs/claude/*.jsonl` contains each unmodified SDK message serialized as one
  JSON object per line. Readable console formatting is not mixed into it.
- `*.session.json` contains only the thread ID, marker, cwd, timestamp, and
  relative create-log path needed for the resume check.
- Claude `*.session.json` records the session ID, marker, cwd, Claude Code
  version, first identity event, and relative create-log paths needed for the
  resume check.
- `*.dormant-zero-turn.json` records the dormant scenario's identity and
  timing evidence. Its sibling JSONL file remains the unmodified provider
  event stream.
- Gate `*.json` files record IDs, errors, markers, turn attribution counts,
  and relative raw-log paths. Failed zero-turn recovery also produces a result
  artifact before the command exits nonzero.
- Claude `*.lifecycle.json` files record streaming mode, the bounded
  post-result window, provider capabilities, message boundaries, and explicit
  lifecycle states or their observed absence.

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
- Claude SDK queries expose no built-in tools, load no filesystem setting
  sources, and use `dontAsk`; the probe verifies all three from `system/init`.
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
