# ATC-232 findings

Status: deterministic gate, isolated real-zmx smoke, and isolated live-provider
composition matrix passing

Observed on 2026-08-22. The live run used private Claude and Codex profiles,
Claude Haiku at low TUI effort, and Codex `gpt-5.6-luna` at low effort. The
prototype now selects and verifies ACP model configuration before sending a
prompt and rejects Claude Fable or Codex Sol at the execution boundary.

## What the unified prototype establishes

The core has no provider branch in its orchestration path. It creates one of
two immutable Thread kinds and dispatches through the kind-specific execution
seam. `chat` lazily materializes one ACP writer on the first prompt. `tui`
lazily materializes one separately persisted Terminal on first open. Reopening
a TUI reuses the linked Terminal identity; it never changes the Thread kind or
adopts a chat provider session.

Public state keeps the dimensions that the earlier experiments showed must be
separate:

- foreground Turn state and outcome;
- agent-owned background activity;
- pending question or approval;
- combined Thread activity;
- Terminal lifecycle and reachability; and
- durable Thread identity after a Turn or Terminal ends.

The combined activity rule is `needs_input > working > unknown > idle`.
Foreground completion cannot force idle over known background work. A pending
background request can be answered without an active Turn. Terminal process
exit forces the Terminal to `ended` and clears stale agent activity without
marking the Thread completed.

The persisted provider identity is private. Startup marks any interrupted
foreground Turn failed with a deterministic recovery reason, then eagerly
loads the exact saved ACP session so an already-pending background request can
still be answered. An adapter error or different returned identity fails
closed; there is no create fallback. One in-memory session slot enforces the
writer rule, and restart tests assert that it is not duplicated.

## Execution topology

ACP v1 is confined to one adapter. Initialization sends an empty client
capability object, so ATC does not implement filesystem or terminal methods.
The adapter owns its exact subprocess, maps provider permission option IDs to
opaque API option IDs, tracks late tool updates as background activity, and
keeps create and exact load separate. The deterministic subprocess test proves
the wire boundary, allow response, normalized assistant/tool events, and
fail-closed load. ATC-229 originally established that both official adapters
honor allow and deny while tools remain agent-owned.

The composed live rerun now adds direct evidence against both pinned adapters.
For Claude, a real file allow and deny, foreground cancellation, exact adapter
restart/load, identity preservation, and context preservation passed on Haiku.
For Codex, the same matrix passed on Luna/low. Codex's automatic reviewer can
approve low-risk writes without consulting the ACP client, so the permission
fixture uses the official adapter's read-only mode, human review, and an
elevated retry to a test-owned localhost server: allow reached it and deny did
not. Claude's current ACP adapter rejects a separate Haiku `effort=low` update;
the verified Haiku selection remains enforced, while Claude TUI accepts its
native `--effort low` flag.

zmx runs only with a private socket and log directory. The public `term_` ID is
also the zmx session name, allowing direct attachment without a proxy. The
child wrapper records atomic start/exit evidence and
forwards HUP/INT/TERM while zmx remains the supervisor. Reconciliation keeps an
unreachable inventory entry, distinguishes missing from stale and exited,
persists stop intent before kill, verifies absence after kill, and refuses
cleanup without complete inventory.

Codex uses one separately launched shared app-server. Every TUI still receives
`--remote`, but connects through a transparent per-TUI relay running inside its
zmx-owned process tree. The relay never originates a conversation method. It
observes the TUI writer's exact `thread/start`, `thread/resume`, or `thread/fork`
response, filters global broadcasts to that root, learns descendants only from
correlated parent `subAgentActivity`, and retries status delivery while the Go
core is down. This removes the ambiguous “newest root wins” failure mode while
letting the shared server and TUIs outlive the core.

Claude receives a deterministic session ID and per-session hook settings that
POST to the linked Terminal ingestion route. Its reducer preserves a specific
question over later generic permission or idle notifications for the same
prompt, uses background-task level snapshots, and subtracts the stopping child
from Claude's stale `SubagentStop` snapshot.

## Automated evidence

`make check` passes formatting, vet, the race detector, and the focused suite.
`make smoke-zmx` also passes against the installed real zmx using a temporary
socket directory, log directory, state directory, and exact test-owned session.
`make smoke-acp` passes against private provider profiles and now performs real
allow, deny, cancellation, fresh-adapter exact load, identity, and context
checks for both providers.

The suite covers:

- both Thread kinds coexisting, wrong-kind typed errors, and local-only create;
- multi-turn one-writer chat, exact interrupt and request correlation;
- ACP empty capabilities, normalized permission IDs, allow response, exact
  create/load separation, and no load-to-create fallback;
- foreground interruption followed by late background-tool evidence;
- a request opened after prompt completion and answered after core restart;
- terminal running, exited, missing, stale, disconnected, inventory failure,
  stop intent, reachable/unreachable orphan, and verified kill;
- Claude question precedence and background descendant transitions;
- Codex exact root selection, unrelated-root rejection, descendant discovery,
  root-idle/child-active aggregation, and exact-read reconstruction;
- process-exit override, cursor reconnect, and restart event de-duplication;
  and
- contract responses containing no ACP names, provider session IDs, zmx names,
  daemon IDs, raw RPC, or hook vocabulary.

Tests wait on channels, event cursors, inventories, and process exits. None use
time sleeps as a success condition.

The reviewed TUI composition run created one Claude and two Codex TUI Threads.
The screens reported Haiku 4.5 and `gpt-5.6-luna low`; all returned distinct
markers. Both Codex relays selected different exact roots and independently
reported `working -> idle`. During a 12-second Codex tool call, the exact core
PID was killed. All three zmx sessions and the shared app-server survived, the
Codex TUI completed while the core was absent, and restart replayed its queued
idle evidence without changing any public Terminal identity. Both Claude and a
second Codex TUI then completed new turns through those same Terminals. Cleanup
ignored the three linked sessions, exact deletion ended them, and the private
zmx inventory was empty afterward.

## Keep, change, discard

Keep:

- immutable Thread kinds with a separately linked Terminal;
- the three small execution seams and one orchestration owner;
- lazy materialization plus eager exact recovery of materialized chat writers;
- independent foreground, background, pending-request, activity, outcome, and
  Terminal dimensions;
- cursor-addressed normalized events paired with private raw evidence;
- evidence-based zmx reconciliation and namespace-scoped cleanup; and
- exact-root Codex relay correlation and stateful Claude/Codex reducers.

Change before production:

- replace the disposable JSON snapshot with transactional repositories and a
  retention policy for events/diagnostics;
- authenticate internal status ingestion and harden relay buffering and health
  reporting;
- make shared Codex app-server lifecycle/profile management an explicit server
  resource instead of a separately launched experiment prerequisite;
- keep terminal transport out of the status/control API until a production
  transport can preserve raw mode and resize semantics;
- define a stronger adapter/process-tree contract for “stop all agent work”;
  foreground ACP cancellation is intentionally not that operation; and
- rerun the live matrix on every pinned provider upgrade, including exact
  restart reconciliation via loaded-list plus exact reads.

Discard:

- the JSON file format, raw HTTP CLI, debug endpoint shape, and unbounded
  in-memory event history;
- direct provider names as launch policy outside configuration; and
- any temptation to infer activity from terminal output or missing inventory.

## Success criteria

1. **One neutral core hosts both paths:** yes in the deterministic composition
   suite; provider selection exists only at execution adapters.
2. **Canonical client control and observation:** yes. HTTP contract tests cover
   create, inspect, kind-specific control, requests, Terminals, cursor catch-up,
   and protocol-field exclusion.
3. **Truthful distinct state under restart:** yes for the automated failure and
   recovery matrix, including late/background evidence and process exit.
4. **Exact ownership survives or fails closed:** yes in deterministic ACP and
   zmx tests, the isolated real-zmx run, both live ACP adapters, and the live
   core-crash TUI run.
5. **Provider topologies compose without leaking:** yes structurally and under
   deterministic and live event streams. The shared-server relay, Claude hook
   reducer, ACP adapter, and zmx adapter ran in one core composition with two
   simultaneously independent Codex roots.
6. **Enough evidence for production design:** yes. The keep/change/discard list
   identifies the durable architecture and the remaining lifecycle, storage,
   transport, and operational work explicitly.

## Decision

Proceed to a production Go-core specification using the domain and seam shape,
not this implementation. Retain exact identity, conservative recovery, and the
separate resource/activity dimensions as invariants. Do not carry forward the
prototype store, raw CLI, unauthenticated ingestion routes, or the unresolved
assumption that foreground cancellation stops agent-owned processes.
