# Provider identity and resume POC

This directory contains deliberately independent provider probes for
[ATC-68](https://linear.app/elevenideas/issue/ATC-68/poc-provider-identity-and-resume-with-codex-app-server-and-claude).
They are experiments, not ATC production session or adapter code.

The POC is complete. See [`findings.md`](findings.md) for reviewed evidence and
the normalized capability matrix, and
[`adapter-recommendation.md`](adapter-recommendation.md) for the final
production adapter, API, and CLI recommendation.

The Codex checkpoints cover conversation creation, same-process turns,
fresh-process resume, dormant and restarted zero-turn behavior, invalid resume
safety, shared-process multiplexing, native-TUI interoperability, structured
input and approval requests, active-turn interruption, and second-client
observer/writer behavior. The
Claude checkpoints cover session creation, a second same-process SDK query,
fresh-process exact-session resume, invalid or missing resume safety with
explicit replacement-session detection, and bounded streaming lifecycle
evidence, including blocking `AskUserQuestion` and harmless Bash permission
requests. Claude control/interoperability coverage also includes active-turn
interruption, concurrent resume behavior, and an Agent SDK → native TUI →
fresh Agent SDK round trip.

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

## Claude active-turn interruption

```sh
pnpm claude interrupt --cwd ../..
```

The probe seeds a durable session, resumes it with only `AskUserQuestion`
exposed, and deliberately holds the request unanswered. After Claude emits
authoritative `requires_action`, the client calls the SDK's `interrupt()`
control method. It records the callback/provider/control timeline, interrupt
receipt, terminal result, and transition back to `idle`.

A fresh SDK query must then resume the exact session ID and cwd and recall the
seed marker. Configure the bounded delay between `requires_action` and the
control request with `--interrupt-delay-seconds <n>`.

## Claude observer and writer behavior

```sh
pnpm claude observer-writer --cwd ../..
```

The local Agent SDK has no read-only API for attaching to another local
query's live event stream. Calling `query({ options: { resume } })` starts an
independent CLI-backed writer instead.

The probe holds writer A at `requires_action`, starts writer B against the same
session ID, then completes A and opens a fresh verifier. It records whether
either client receives the other's event UUIDs or marker, whether both writers
complete, and which markers remain visible after a fresh resume. This is a
negative-capability experiment: an unsupported observer attachment or unsafe
writer concurrency is a reviewed outcome, not a probe failure.

## Claude native-TUI round trip

This command requires an interactive terminal:

```sh
pnpm claude tui-round-trip --cwd ../..
```

It creates and seeds a session through the Agent SDK, closes that client, and
opens the exact ID with the native `claude --resume` TUI in safe mode with no
tools or filesystem settings sources. After the marker response appears,
press Ctrl-D twice to exit. A fresh Agent SDK query then verifies the exact
session ID, cwd, and native-TUI marker continuity.

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

## Codex input request

```sh
pnpm codex input-request --cwd ../..
```

`request_user_input` is unavailable in Codex Default mode. The probe opts into
App Server's experimental API, reads the advertised Plan-mode preset with
`collaborationMode/list`, and applies that preset to the bounded turn. It then:

1. holds the correlated `item/tool/requestUserInput` server request;
2. requires `thread/status/changed` to report
   `activeFlags: ["waitingOnUserInput"]` while the request is pending;
3. validates the exact question and ordered choices;
4. responds to the request ID with `Alpha`; and
5. requires the same turn to use the answer and complete.

Use `--hold-seconds <n>` to change the observable pending interval.

## Codex permission request

```sh
pnpm codex permission-request --cwd ../..
```

The probe asks Codex to run exact `pwd` with an explicit sandbox escalation.
It accepts only the provider's exact shell-normalized
`/bin/zsh -lc pwd` representation with one structured `pwd` action and the
expected cwd. While the request is pending, the provider must report
`waitingOnApproval`. The client then returns `{ decision: "accept" }` to the
correlated request ID and requires the command and turn to complete without a
repository change.

## Codex active-turn interruption

```sh
pnpm codex interrupt --cwd ../..
```

The scenario seeds resumable context, starts a bounded foreground `sleep 30`
command, waits until its `commandExecution` item is active, and calls
`turn/interrupt` with the exact thread and turn IDs. It requires:

- a successful interrupt response;
- terminal `turn/completed` status `interrupted`;
- the interrupted turn in fresh-process resumed history; and
- exact ID, cwd, and seed-marker continuity in a subsequent turn.

## Codex observer and writer behavior

```sh
pnpm codex observer-writer --cwd ../..
```

Writer A is held on a structured Plan-mode input request while a second App
Server resumes the exact thread. The second client records the active snapshot,
attempts a concurrent marker turn, and checks whether writer A's later events
are fanned out live. After both clients stop, a third App Server records the
persisted turn and marker attribution and verifies exact-session continuity.

Reviewed behavior on `codex-cli 0.145.0`: the second client can resume and
write, but receives no live events from writer A. Both writes can complete,
yet fresh history can detach an agent response from its original provider turn
into a synthetic rollout turn. Treat second-client live observation as
unsupported and enforce one active writer per Codex thread.

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
- Claude interruption artifacts include the terminal result, interrupt receipt,
  lifecycle transitions, and a derived callback/provider/control timeline.
- Claude observer/writer artifacts keep separate raw streams for both
  concurrent clients and the fresh resume verifier.
- Codex input and permission artifacts record request IDs, provider sequence,
  pending-state flags, response timestamps, and terminal turn state.
- Codex interruption artifacts record the exact control target, interrupt
  receipt timing, terminal `interrupted` state, and fresh resume evidence.
- Codex observer/writer artifacts keep separate streams for writer A, the
  second client, and the fresh resume verifier, plus persisted marker-to-turn
  attribution.

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

## Held-connection experiments (ATC-83)

These scenarios sharpen the one-writer rule: they hold the adapter's
connection open while a separate provider process (the TUI-equivalent
resume path) drives the same conversation. Findings live in
[`held-connection-findings.md`](held-connection-findings.md).

```sh
pnpm codex:held passive-hold
pnpm codex:held stale-write
pnpm codex:held poll-observe
pnpm claude:held passive-hold
pnpm claude:held stale-write
```

- `passive-hold` — the held connection never writes while two external
  turns run; verifies it observes nothing, that history stays intact
  across an adapter restart, and (Codex) that `thread/read` and
  `thread/unsubscribe` work on the held connection.
- `stale-write` — the held connection writes once, serialized, after an
  external turn; characterizes context blindness and (Claude) history
  divergence.
- `poll-observe` — Codex only; polls `thread/read` during an active
  external turn to measure visibility latency and whether `thread.status`
  reflects another process's activity (it does not).

External writers use `codex exec resume` and `claude -p --resume`, the
same separate-process resume paths as the native TUIs. Each run writes a
JSON evidence record plus raw JSONL streams under `runs/`.

## Safety and failure behavior

- Thread and turn requests use the read-only sandbox and approval policy
  `never`.
- Prompts explicitly prohibit tools, commands, and file changes.
- Server-initiated requests are rejected by default. The input and permission
  scenarios install one exact fail-closed handler and reject any other method,
  duplicate request, command, cwd, question, or option shape.
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
