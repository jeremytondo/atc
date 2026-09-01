# ATC-285 thread-status probe

This experiment tests one boundary only: an unmodified T3 Code environment's
authenticated shell snapshot can make newly created T3 threads appear in ATC's
status vocabulary without persistence, background services, or T3 lifecycle
mutation.

The wrapper mints a five-minute T3 pairing grant, exchanges it for exactly the
`orchestration:read` scope, and revokes the resulting access session on normal
exit. It never starts, stops, or configures the T3 server. Like any process
cleanup, revocation cannot run after an uncatchable hard kill; `--duration` is
the safest way to run a bounded probe.

## Run once

```sh
./scripts/atc-285 snapshot --project-root "$PWD"
```

The probe emits one JSON object per thread. `nativeStatus` preserves the T3
session evidence while `status` is the ATC projection. `workspaceRoot` is the
T3 project's default directory; `cwd` is the thread's `worktreePath` when set,
otherwise that workspace root.

## Watch thread creation and status changes

```sh
./scripts/atc-285 watch --project-root "$PWD"
```

Leave the probe running, create a thread in T3 Code for that project, and start
a turn. The probe first emits `present` records, then `created`,
`status_changed`, and `removed` records as successive shell snapshots differ.
Press Ctrl-C to stop and revoke the temporary credential.

For the live Effect RPC subscription instead of polling:

```sh
./scripts/atc-285 watch-ws --project-root "$PWD"
```

This consumes T3's initial snapshot and ordered shell events. On disconnect it
requests a fresh one-use WebSocket ticket and resumes after the last applied
sequence; T3 can replay the gap or replace it with a fresh snapshot.
Add `--duration 10s` for a bounded smoke test that exits and revokes its
temporary credential without requiring an interrupt.

## Status precedence

1. Pending approval becomes `waiting_for_permission`.
2. Pending user input becomes `waiting_for_input`.
3. A starting/running session or live background work becomes `working`.
4. A session error becomes `error`.
5. Known resting session states become `idle`.
6. New T3 lifecycle values become `unknown` rather than being guessed.

Approval and input intentionally outrank the underlying session status because
they describe the action that unblocks the thread. Session error intentionally
outranks stale background liveness.

## Tests

```sh
go test -race ./experiments/atc-285/probe
```

The fixture sequence starts empty, introduces a running thread, and transitions
it through permission, input, error, and idle. Separate tests cover an expired
credential, an unavailable server, and an incomplete response schema.
