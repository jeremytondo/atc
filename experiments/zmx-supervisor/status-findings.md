# ATC-231 findings

Tested on 2026-08-22 with Go 1.26.4, zmx 0.6.0, Claude Code 2.1.237,
and Codex 0.149.0. The Codex run used isolated `ZMX_DIR` and `CODEX_HOME`
directories and its exact session was stopped and cleaned up afterward.

## Exercised matrix

| Condition | Reliable normalized result | Authority and raw evidence |
| --- | --- | --- |
| Provider child settled before lifecycle evidence arrived | `unknown` | Process inventory proved only that the zmx daemon and child were running. |
| Claude accepted a prompt | `working` | `UserPromptSubmit`, correlated by provider session and prompt ids. |
| Claude completed a harmless turn | `idle` | `Stop`, including the last assistant message and provider session id. |
| Claude blocked on `AskUserQuestion` | `waiting_for_input` | `PermissionRequest` identified the specific tool and retained the complete payload. |
| Claude emitted a later generic permission reminder for that question | `waiting_for_input` | The stateful reducer preserved the more specific outstanding request for the same prompt. |
| Claude blocked on a general tool approval | `waiting_for_permission` | `PermissionRequest` identified a non-question tool. |
| Codex displayed its startup trust dialog | `unknown` | No structured adapter was present; process evidence did not guess from the screen. |
| Codex displayed its ready composer | `unknown` | The ready glyph is not treated as lifecycle evidence. |
| Codex completed a short no-tools turn between polls | `unknown` while running | Screen polling missed the working interval, demonstrating that absence of an indicator proves nothing. |
| Codex displayed an interactive question | `unknown` | The question UI reused prompt and interrupt text that screen rules had misclassified. |
| ATC deliberately stopped a child | `completed` | Persisted stop intent and the exit marker override stale provider evidence. Unexpected nonzero exits normalize to `failed`. |
| zmx inventory was unavailable | `unavailable` in deterministic test | Process evidence records the inventory failure while the underlying supervisor error still surfaces. |

The race-enabled suite covers the observed Claude event ordering, structured
state reduction, generic-notification de-duplication, generated hook settings,
process completion and failure, Codex's conservative running state, and
unavailable inventory.

## Conclusions

1. **Claude lifecycle hooks are useful, but individual events are not a state
   machine.** A specific `PermissionRequest` for `AskUserQuestion` was followed
   by a generic `Notification.permission_prompt`. Selecting the newest event
   changed the correct `waiting_for_input` state into
   `waiting_for_permission`. Replaying correlated events fixes that: generic
   reminders cannot replace a more specific outstanding state for the same
   prompt.
2. **Terminal screens are presentation, not protocol.** The Codex trust dialog,
   ready composer, and question dialog reuse prompt glyphs and generic footer
   text. A rule that recognized one state produced a false result for another.
   Short turns can also start and finish between polls. Screen inference is
   therefore absent from the automatic status path, rather than labeled as a
   fallback.
3. **Unknown is the correct Codex result for now.** Process evidence reliably
   distinguishes running, completed, failed, and unavailable. It cannot say
   whether a running Codex TUI is idle, working, or blocked, so the prototype
   does not invent that distinction.
4. **Process exit is final.** Once the child exits, its marker overrides stale
   structured evidence so a previous `working` or waiting state cannot mask
   completion or failure.
5. **The provider boundary remains small.** The supervisor supplies provider
   kind and process evidence. `internal/agentstatus` owns launch
   instrumentation, raw signals, provider-specific reduction, normalization,
   and transition history.

## Recommendation

Carry the structured-or-process model into the unified Go core. Keep the
stateful Claude hook reducer. Add rich Codex states only with a dedicated,
structured `codex app-server` adapter whose lifecycle belongs to ATC; until
then, a running Codex session should remain `unknown`.
