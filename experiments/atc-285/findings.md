# ATC-285 findings

Status: the contained thread-discovery and status-projection slice passed.

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

## Conclusion

The simplest ATC-285 premise is viable: T3's lightweight shell snapshot is
enough to discover durable thread identities and derive the six ATC statuses
without transcript reads, persistence, or mutation. Polling successive
snapshots is sufficient for the proof because T3 threads are durable; a
production endpoint can remain on-demand as the issue proposes.

This does not yet prove ATC server composition, credential storage or refresh,
project deduplication, streaming, or promotion into `/v1/threads`. Those remain
outside this experiment.
