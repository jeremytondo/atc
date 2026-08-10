# ATC-158 probe findings (2026-08-10)

Recorded against Claude Code 2.1.226 (SDK 0.3.220) and codex-cli 0.147.0.
Raw evidence: `claude/recording.jsonl`, `codex/recording.jsonl`,
`codex/read-probe.jsonl`, `codex/reconcile-probe.jsonl`.

## Claude

Confirmed:

- Root `Stop` fires with a non-empty `background_tasks` level snapshot while
  a background subagent is running (`{id, type: "subagent", status:
  "running", description, agent_type}`), and the turn's `result: success`
  arrives right after. Backgrounded Bash produces the same shape with
  `type: "shell"` and a `command` field.
- Hooks fired from inside a subagent carry `agent_id` (`PreToolUse`,
  `PostToolUse`, `SubagentStart`, `SubagentStop`); root-origin hooks do not.
- `SubagentStart` carries the new subagent's `agent_id`.
- After the last background descendant finishes, the session runs a wake
  turn (its own `UserPromptSubmit` … `Stop`), and that `Stop` carries
  `background_tasks: []` — the last-child idle transition arrives as hook
  evidence on both transports without any client turn.
- The SDK emits `result` messages for wake turns too, and can hold a client
  turn's `result` back while background work runs. Deriving `idle` from
  every success `result` is therefore wrong; `session_state_changed: idle`
  fires only after the background do-while drains (matches its doc).
- The SDK stream also carries `system/background_tasks_changed` level
  snapshots (`tasks: [{task_id, task_type: local_agent|local_bash, ...}]`);
  the `task_id` values match the hook snapshots' `id` values.

Deltas from the issue's assumptions:

- **`SubagentStop`'s level snapshot still contains the stopping agent**
  (status `running`). The tracker must subtract the entry whose `id`
  equals the payload's `agent_id`; the snapshot alone does not encode the
  last-child transition.
- No cron/wakeup tool was reachable in the SDK session context, so
  `session_crons` remains typed evidence from the SDK d.ts (`{id, schedule,
  recurring, prompt}`); no live payload was recorded.

## Codex

Confirmed:

- Descendant `thread/status/changed` fans out to every connected socket
  without subscription, exactly like root threads'.
- A spawned descendant runs while its parent's turn completes: parent
  `thread/status/changed { idle }` + `turn/completed` arrive with the child
  still `active` — the premature-idle scenario is real.
- `thread/read {threadId}` returns the full thread with `parentThreadId`
  and live `status` for descendant ids, from any fresh connection.
- `thread/loaded/list` returns the ids of sessions currently loaded in
  memory: while the child ran (parent idle), a fresh connection got BOTH
  ids; `thread/read` on each yields `{parentThreadId, status}` — the
  demand-driven reconnect reconciliation path.

Deltas from the issue's assumptions:

- **Descendant threads do NOT broadcast `thread/started`.** The child→root
  mapping arrives instead as a `subAgentActivity` item on the PARENT's
  feed: `item/started`/`item/completed` with `item: {type:
  "subAgentActivity", kind: started|interacted|interrupted, agentThreadId,
  agentPath}`. (`captureStarted` mis-adoption by a subagent broadcast is
  therefore not currently reachable, but the guard stays as cheap defense.)
- **`thread/list` omits descendant threads entirely** (both populations),
  so the issue's planned thread/list walk cannot rebuild the descendant
  graph; `thread/loaded/list` + per-id `thread/read` replaces it.
- `Thread.sessionId` is inconsistent for descendants across runs (own id in
  one recording, parent's id in another) — confirming the issue's decision
  not to rely on it.
