# ATC-229 findings

Status: blocked on provider ACP v2 support

Observed on 2026-08-22 against ACP schema `2.0.0-alpha.3`, released
2026-08-20.

## Real provider negotiation

The host launched each official adapter through its published npm package and
sent the same v2 initialization request:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "protocolVersion": 2,
    "capabilities": {},
    "info": {
      "name": "atc-acp-v2-host",
      "title": "ATC ACP v2 experiment",
      "version": "0.1.0"
    }
  }
}
```

| Provider | Official adapter | Reported version | Negotiated protocol | Result |
| --- | --- | --- | --- | --- |
| Codex | `@agentclientprotocol/codex-acp` | `1.6.2` | `1` | v2 unavailable |
| Claude | `@agentclientprotocol/claude-agent-acp` | `0.70.0` | `1` | v2 unavailable |

Both adapters advertised substantial v1 capabilities, including session
resume/list/close and permissions, but neither returned v2 capability shapes
or v2 lifecycle semantics. The host rejected the downgrade as required. It did
not call any v1 session method, create a provider session, or fall back to the
providers' non-ACP APIs.

## What the host-side prototype establishes

The deterministic child-agent integration test covers the complete host-side
v2 path:

- launch and supervise a stdio ACP subprocess;
- initialize at exactly protocol v2 and inspect capabilities;
- create a session and run multiple prompts;
- stream assistant, activity, and state updates;
- hold and allow a permission request using its provider request ID and exact
  offered option ID;
- cancel active work and wait for the authoritative idle/cancelled state;
- close the session and child process cleanly;
- atomically persist the minimum session metadata;
- start a fresh agent process, resume the exact session ID, and apply replayed
  history before the resume response;
- record raw frames and normalized records in one JSONL timeline.

This proves the Go harness and the proposed narrow adapter boundary. It does
not substitute for evidence from real provider implementations.

## Success criteria answers

1. **Can Go reliably own the ACP v2 process and lifecycle for Claude and
   Codex?** Host-side process ownership works in the subprocess integration
   test. The provider-specific answer is not yet testable because both real
   adapters negotiate v1.
2. **Does ACP v2 expose enough information and control for ATC native chat?**
   On paper and in the fake-agent run, yes: explicit running, idle,
   requires-action, permission, cancellation, output, and activity signals map
   cleanly. This remains unverified against real Claude and Codex behavior.
3. **Can sessions resume after the ATC host restarts?** The host persists and
   resumes an exact ID with replay in a fresh subprocess, with no new-session
   fallback. Real-provider v2 resume remains unverified.
4. **Can provider differences stay behind a small normalized model?** The host
   currently has no provider-specific lifecycle logic; provider selection only
   chooses a launch command. Real differences cannot be evaluated until both
   providers expose v2.
5. **Is v2 instability isolated cheaply?** Yes for the prototype structure.
   All v2 wire names and shapes live under `internal/acp/`; the harness depends
   only on normalized callbacks and operations. Moving from the prior alpha to
   `2.0.0-alpha.3` did not require provider logic outside that boundary because
   none exists.

## Decision

Do not proceed to the ATC-228 Go + zmx supervision prototype on the strength of
ACP v2 yet. Re-run this experiment when the official Claude and Codex adapters
both negotiate protocol 2. The next gate is the same real-provider matrix:
multi-turn continuity, allow and deny, cancellation, clean shutdown, crash
recovery, and exact-session resume with replay.
