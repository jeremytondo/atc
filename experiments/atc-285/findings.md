# ATC-285 findings

Status: the contained thread-discovery, status-projection, and live-subscription
slices passed.

Observed on 2026-09-01 against the developer's already-running T3 Code
`0.0.38-nightly.20260831.1236` environment. The experiment did not start,
stop, or mutate its orchestration lifecycle.

## Live result

An authenticated `GET /api/orchestration/shell` returned the T3 thread created
for this experiment in the expected `atc` project and workspace root. Its T3
session was `running`; the probe projected it as ATC `working` on the first
fetch.

The ambient `T3_MCP_BEARER_TOKEN` was correctly rejected by the orchestration
endpoint. A five-minute pairing credential could be exchanged down to exactly
`orchestration:read`, used for the snapshot, and revoked afterward. A follow-up
inspection found no probe pairing grants or sessions left behind.

The live `orchestration.subscribeShell` subscription also returned this thread
in its initial snapshot. The thread had no separate worktree, so T3's
`worktreePath ?? project.workspaceRoot` rule produced the ATC repository root
as its effective `cwd`.

## Deterministic result

The fixture sequence starts without threads, introduces one running T3 thread,
and moves it through pending approval, pending user input, session error, and
ready. Snapshot differences reported one creation followed by the expected ATC
statuses:

`working -> waiting_for_permission -> waiting_for_input -> error -> idle`

Unknown T3 session or background-liveness values project to `unknown` rather
than a guessed resting state. Missing fields needed by the projection are
reported as schema errors. Unavailable-server and expired-credential failures
are reported separately.

A WebSocket fixture forced the connection closed after sequence 2. The probe
requested a new one-use ticket, resubscribed with `afterSequence: 2`, and
applied the replayed permission-waiting update at sequence 3 without emitting a
duplicate creation. It speaks only the Effect RPC pieces this stream requires:
`Request`, streamed `Chunk`, `Ack`, `Exit`, and reconnect.

The bearer session issued by T3 outlives the five-minute pairing grant, so
cleanup matters. A bounded live smoke test exited normally and left zero probe
sessions, pairing grants, processes, or temporary binaries. An uncatchable hard
kill can still prevent best-effort revocation; production credential issuance,
refresh, and recovery remain unresolved rather than being hidden in this
adapter.

## Conclusion

The ATC-285 premise is viable: T3's lightweight shell snapshot is enough to
discover durable thread identities and derive the six ATC statuses without
transcript reads, persistence, or mutation. Its shell subscription adds
near-real-time changes and gap recovery. Effect RPC is narrow, testable
coupling—not a blocker—but it does require a long-lived observer and in-memory
view that the on-demand HTTP endpoint does not.

This does not yet prove ATC server composition, credential storage or refresh,
or promotion into `/v1/threads`. Project association now has a concrete rule:
join `thread.projectId` to T3's project, canonicalize its `workspaceRoot`, and
match that to ATC's canonical project directory. Those production steps remain
outside this experiment.
