# ACP Go host experiment (ATC-229)

This is an isolated Go prototype for testing ACP v1 as the provider boundary
for ATC native-chat Threads. It launches the official Claude or Codex ACP
adapter as a supervised stdio subprocess. It does not import or modify the
current app server.

The wire protocol is isolated under `internal/acp/`. The harness above it only
sees provider-neutral sessions, state, permissions, output, tool activity, and
terminal outcomes.

## Current result

As of 2026-08-22, the official Codex adapter 1.6.2 and Claude adapter 0.70.0
both negotiate ACP v1. Real runs passed session creation, multi-turn prompts,
streamed messages and tool activity, cancellation, clean shutdown,
fresh-process history replay, and follow-up context continuity with the exact
same session ID. See [findings.md](findings.md) for the matrix and remaining
limitations.

The host advertises no client filesystem or terminal capabilities. Real
read/edit/command and permission allow/deny tests confirmed that the agents own
execution while ACP carries control and observation. Turn cancellation does not
necessarily terminate agent-owned tool processes, and background work can
outlive a prompt; those lifecycle cases remain explicit follow-ups.

## Build and test

Requirements are Go 1.26 or newer, `npx`, and locally authenticated Codex and
Claude Code installations for real prompts.

```sh
cd experiments/acp-host
make check
make build
```

The tests launch a deterministic fake ACP agent as a real child process and do
not need provider credentials or network access.

## Run real adapters

Probe initialization without creating a session:

```sh
./bin/atc-acp --provider codex --probe --cwd ../..
./bin/atc-acp --provider claude --probe --cwd ../..
```

Open an interactive session:

```sh
./bin/atc-acp --provider codex --cwd ../..
./bin/atc-acp --provider claude --cwd ../..
```

The provider defaults are the official packages:

```text
npx -y @agentclientprotocol/codex-acp
npx -y @agentclientprotocol/claude-agent-acp
```

Use `--provider custom --agent /absolute/path/to/agent` and repeated
`--agent-arg` flags for another ACP v1 adapter.

Plain text in the REPL sends a prompt. `:status`, `:meta`, and `:permissions`
inspect the normalized state; `:approve`, `:deny`, and `:cancel` control an
active turn; `:raw on` displays the underlying JSON-RPC; and `:exit` closes the
session and child process. `:crash` deliberately exits without cleanup for
recovery testing.

`--permissions allow` and `--permissions deny` automatically select the first
matching option offered by an agent. The default is interactive `ask`.

## Persistence and replay

By default, metadata and JSONL traffic are stored under:

```text
.atc-acp/<provider>.json
.atc-acp/<provider>.jsonl
```

The state file contains launch metadata, negotiated capabilities, and the
session ID—not credentials or the conversation transcript. The JSONL file
records raw frames and normalized events on one timeline. Override either path
with `--state` or `--log`.

On startup, a saved session for the same provider and working directory is
restored with `session/load`, which replays its history. Pass `--replay=false`
to use `session/resume` without replay. Restore failures are surfaced instead
of silently creating a new session; use `--new` to do that deliberately.

## Normalized lifecycle

ACP v1 keeps `session/prompt` pending for the whole turn. The host enters
`working` when that request is sent and returns to `idle` only when the response
provides a stop reason. A `cancelled` stop reason records a cancelled outcome;
other successful stop reasons record completion. Permission requests
temporarily enter `waiting_for_permission`, and JSON-RPC or process failures
become `failed` or `disconnected`.

Assistant message chunks become output events. Thought, plan, usage, command,
and tool-call updates remain activity events. The raw update is retained in the
JSONL log even when the normalized display is intentionally terse.
