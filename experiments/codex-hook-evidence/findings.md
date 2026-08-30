# Codex hook evidence findings

Status: complete

Date: 2026-08-30  
Environment: `codex-cli 0.151.0`, Linux, private `CODEX_HOME`, disposable
workspace and Unix sockets

## Verdict

| Question | Verdict | Conclusion |
| --- | --- | --- |
| Thread identity | Pass, with lifecycle caveats | `session_id` is exact, stable across events, and survives native-TUI and shell resume. Use the payload field, not `CODEX_THREAD_ID`. |
| Coarse status | Partial | Hooks reliably mark prompt submission, permission wait, tool boundaries, stop, interrupt, and session end. |
| Authoritative live status | Fail | No hook marks approval resolution or tool execution start, and an interrupted foreground tool can finish later and emit `PostToolUse`. Hooks alone cannot keep an exact real-time state reducer correct. |
| Injection | Pass | A launch profile is sufficient; project and global ATC hooks were not needed. |
| Coexistence and cleanup | Pass | The pre-existing Herdr `SessionStart` hook and the profile hook both ran. Environment gating suppressed ATC reports, and deleting the profile/private home removes the experiment. |

Recommendation: **no-go for hook-only authoritative status**. Proceed with
hooks as identity and coarse-lifecycle evidence, provided ATC treats payload
`session_id` as authoritative and supplements approval/tool state from the
app-server protocol or another control-plane source. The identity evidence by
itself is strong enough to use.

## Injection and trust

The first and least invasive attempted form worked: a launch profile named
`atc-281`, selected with `codex -p atc-281`. The footprint was one profile in
the private Codex home plus the capture executable in this experiment. No
project-scoped or global ATC hook was installed.

On the first launch, Codex discovered all eight profile hooks but held them
inactive behind a **Hooks need review** prompt. The pre-existing trusted Herdr
hook remained active. Choosing **Trust all and continue** appended eight
`hooks.state` hashes to the profile file itself; the base `config.toml` was
byte-for-byte unchanged. Trust is therefore tied to each exact hook definition
and profile path. Removing the profile removes both the hook declarations and
their trust entries.

`--dangerously-bypass-hook-trust` ran the same hooks after a warning and did
not write trust state. It is suitable for a bounded test but not a production
launch contract.

The capture executable required both `ATC_HOOK_CAPTURE_FILE` and
`ATC_HOOK_TEST_ID`. A control session with the capture path present but the
test-ID gate absent invoked the hooks and produced no capture file.

## Identity observations

All hook payloads included `cwd`, `session_id`, and `transcript_path`.
`SessionStart` also included `source`. In every captured hook subprocess,
`CODEX_THREAD_ID` was absent, including fresh, in-TUI resume, fork, and shell
resume paths.

| Path | Session ID | `SessionStart.source` | Result |
| --- | --- | --- | --- |
| Fresh native TUI | `01a0536f-db48-7643-bb41-2d2193eb8826` | `startup` | New exact ID |
| In-TUI `/new` | `01a05371-2ca8-7871-a058-3ffe6dc77e89` | `startup` | New exact ID |
| In-TUI `/resume` of the first thread | `01a0536f-db48-7643-bb41-2d2193eb8826` | `resume` | Original ID restored |
| In-TUI `/fork` | `01a05372-af7c-74d3-ba1f-429d8cf91003` | `startup` | New exact ID |
| Shell `codex resume <fork-id>` | `01a05372-af7c-74d3-ba1f-429d8cf91003` | `resume` | Fork ID restored in a fresh process |

`startup` does not distinguish fresh launch, `/new`, or `/fork`. Hooks prove
the active identity but do not explain how a new identity was derived. Also,
`SessionStart` was deferred until the first submitted prompt; merely opening a
zero-turn TUI produced no hook evidence.

Exiting the multi-thread TUI emitted `SessionEnd` for all three threads it had
opened or created, not only the active fork. Every observed end reason was
`other`. A consumer must key ends by `session_id` and must not treat one TUI
exit as one active-thread-only end.

## Status observations

The approval turn on fork thread `01a05372-af7c-74d3-ba1f-429d8cf91003`
produced this order for one stable `turn_id`:

```text
UserPromptSubmit -> PreToolUse -> PermissionRequest -> PostToolUse -> Stop
```

The permission prompt was held until interactive approval. There was no hook
for **approval granted**, **request resolved**, or **tool execution started**.
In this run, `PermissionRequest` preceded `PostToolUse` by 16.084 seconds.
During the post-approval command interval, a hook-only reducer would still
show `needs_input` unless it guessed from time or another signal.

The interrupt turn produced:

```text
UserPromptSubmit -> PreToolUse -> Interrupt -> PostToolUse
```

After Escape, the TUI immediately displayed **Conversation interrupted**, but
the test-owned `sleep 30` command continued. `PostToolUse` arrived 18.923
seconds after `Interrupt`; no `Stop` arrived for that turn. `Interrupt` must be
treated as terminal for conversational state, and late tool events must not
resurrect the turn. The late completion also means the hook stream cannot by
itself say when all execution has actually ceased.

For ordinary turns, `UserPromptSubmit` is a useful working edge and `Stop` a
useful completed/idle edge. `PermissionRequest` is a reliable pending-approval
edge. These are useful coarse observations, not a complete authoritative
state machine.

## Cross-client and listing observations

A fresh shell process resumed the fork with
`codex resume 01a05372-af7c-74d3-ba1f-429d8cf91003`. Its first hook payload
reported the exact same ID, the same transcript path, the same cwd, and
`source: "resume"`. The subsequent prompt and stop retained that ID.

For the listing check, a private app server was started on a test-owned Unix
socket and a remote TUI created a live thread
`01a05377-8163-7e13-8896-877f972b77e6`. `codex agents --remote <socket>`
listed that exact task under `/tmp/atc-281-workspace`. It did not list the
simultaneously open ordinary local TUI, because `codex agents` is scoped to
the app-server daemon it connects to. It is viable for ATC-managed remote
sessions, not for discovering every unrelated native Codex TUI on a machine.

The launch profile is a client-side option in the remote form tested; it did
not install the capture hook into the already-running server. Server-side
hook configuration would need to be supplied when starting the managed app
server.

## Herdr coexistence

The copied, already-trusted Herdr `SessionStart` hook and the ATC profile
`SessionStart` hook both executed for each gated native-TUI path. A test-owned
Herdr socket received the same `agent_session_id` and start source seen by the
ATC capture for fresh, `/new`, `/resume`, `/fork`, and shell-resume scenarios.
Neither hook suppressed or replaced the other.

The real Herdr socket, real Codex app server, and real global Codex files were
never used as test targets.

## References

- [Codex hooks documentation](https://learn.chatgpt.com/docs/hooks)
- [`codex-cli 0.151.0` hook discovery source](https://github.com/openai/codex/blob/rust-v0.151.0/codex-rs/hooks/src/engine/discovery.rs)
- [`codex-cli 0.151.0` hook runtime source](https://github.com/openai/codex/blob/rust-v0.151.0/codex-rs/core/src/hook_runtime.rs)

## Evidence

[`samples.jsonl`](samples.jsonl) contains exact captured records for fresh,
new, resume, fork, approval, interrupt, end, and fresh-process resume paths.
Each record preserves the original payload, timestamp, test gate values, and
the observed null `CODEX_THREAD_ID` value.
