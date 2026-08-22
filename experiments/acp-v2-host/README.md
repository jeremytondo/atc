# ACP v2 Go host experiment (ATC-229)

This is an isolated Go prototype for testing draft ACP v2 as the provider
boundary for ATC native-chat Threads. It does not import or modify the current
app server.

The protocol boundary is entirely under `internal/acp/`. The rest of the
prototype only sees a small normalized state model, provider/session metadata,
and events. The implementation targets ACP v2 exclusively and deliberately
rejects a v1 negotiation instead of silently changing semantics.

## Current result

As of 2026-08-22, the latest official Claude and Codex adapters both negotiate
ACP v1 when sent an ACP v2 `initialize` request. The harness therefore stops at
the version gate for both real providers. See [findings.md](findings.md) for the
exact versions, observed responses, and answers to the issue's success
questions.

The remaining v2 lifecycle is exercised against a deterministic subprocess in
the test suite. This keeps the harness ready to rerun when either official
adapter adds v2 while proving that process ownership, v2 request routing,
state normalization, permission responses, cancellation, persistence, and
resume/replay work together on the host side.

## Build and test

Requirements:

- Go 1.26 or newer
- `npx` for the default official adapter launch commands
- locally authenticated Claude Code and Codex installations for real prompts

```sh
cd experiments/acp-v2-host
make check
make build
```

The test suite launches its fake ACP agent as a real child process; it does not
need provider credentials or network access.

## Probe real adapters

These commands send `protocolVersion: 2` and exit on any downgrade:

```sh
./bin/atc-acp --provider codex --probe --cwd ../..
./bin/atc-acp --provider claude --probe --cwd ../..
```

The provider defaults resolve through the official packages:

```text
npx -y @agentclientprotocol/codex-acp
npx -y @agentclientprotocol/claude-agent-acp
```

Use an installed or custom agent executable without a shell wrapper:

```sh
./bin/atc-acp \
  --provider custom \
  --agent /absolute/path/to/acp-v2-agent \
  --agent-arg value \
  --cwd /absolute/path/to/project
```

## Interactive harness

Once an agent negotiates v2, omit `--probe` to create or resume a session and
open the REPL:

```sh
./bin/atc-acp --provider codex --cwd ../..
```

Plain text sends a prompt. Commands are:

```text
:status             normalized ATC-style state
:meta               provider capabilities and durable session metadata
:permissions        pending permission requests and offered options
:approve [id]       select the first allow option
:deny [id]          select the first reject option
:cancel             cancel active work and pending permissions
:raw on|off         toggle raw JSON-RPC display
:exit               session/close followed by clean process shutdown
:crash              os.Exit without cleanup, for recovery testing
```

`--permissions allow` and `--permissions deny` provide deterministic automatic
permission policies. The default is interactive `ask`.

By default, metadata and logs are written under the target working directory:

```text
.atc-acp/<provider>.json
.atc-acp/<provider>.jsonl
```

The state file contains only the provider name, launch command, cwd, negotiated
protocol and capabilities, and session ID. It contains no credentials or
conversation transcript. The JSONL file records every raw inbound/outbound ACP
frame plus corresponding normalized events and state transitions. Override the
paths with `--state` and `--log`.

On startup, an exact saved session for the same provider and cwd is resumed
with `session/resume` and `replayFrom: {"type":"start"}`. There is no fallback
to `session/new` if resume fails, because that would hide lost continuity. Use
`--new` deliberately to replace the saved session metadata with a new session.

To exercise unclean recovery:

1. Complete at least one prompt so the provider has durable history.
2. Run `:crash`.
3. Start the same command again.
4. Confirm `:meta` shows the same session ID and the replayed history appears.
5. Ask a follow-up that depends on the earlier turn.

## Normalized model

The current state is one of `connecting`, `working`, `idle`,
`waiting_for_permission`, `waiting_for_input`, `failed`, or `disconnected`.
Terminal outcomes are retained separately as `lastOutcome` and
`lastStopReason`, so a completed or cancelled turn can become idle without
losing why it ended.

ACP v2 mappings are intentionally small:

| ACP v2 signal | Normalized result |
| --- | --- |
| `state_update.running` | `working` |
| `state_update.requires_action` with a pending permission | `waiting_for_permission` |
| `state_update.requires_action` without one | `waiting_for_input` |
| `state_update.idle` | `idle` plus terminal outcome/stop reason |
| `session/request_permission` | pending permission with exact options |
| agent/thought/tool updates | assistant output or activity event |
| JSON-RPC/process error | `failed` or `disconnected` |

The prompt response is treated only as acceptance. Completion comes solely
from the v2 idle `state_update`, including cancellation confirmation.
