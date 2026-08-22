# ATC-231 findings

Tested on 2026-08-22 with Go 1.26.4, zmx 0.6.0, and a real authenticated
Claude Code 2.1.237 TUI. zmx ran in a throwaway private directory and the exact
test session was stopped and cleaned up afterward.

## Exercised matrix

| Condition | Normalized result | Authority and raw evidence |
| --- | --- | --- |
| zmx child settled before the TUI rendered | `unknown` | Process inventory reported a running daemon without richer evidence. |
| Claude ready for a prompt | `idle` | Screen fallback matched the current `❯` prompt line. |
| Harmless no-tools turn completed | `idle` | Claude's `Stop` hook payload replaced the screen source as authoritative and included the exact provider session id. |
| Claude blocked on `AskUserQuestion` | `waiting_for_input` | Claude's `PermissionRequest` hook identified the tool and preserved the complete payload. |
| ATC deliberately stopped the child | `completed` | The persisted stop intent and exit marker overrode the stale waiting hook. Unexpected nonzero exits normalize to `failed`. |
| zmx inventory unavailable | `unavailable` in deterministic test | Process fallback records the inventory failure without preventing the underlying supervisor error from surfacing. |
| Structured signal conflicts with screen text | Structured signal wins in deterministic test | The observer stops at the first usable source instead of merging candidates. |

The race-enabled suite also covers Claude hook mapping for working, input,
permission, idle, and completion; Codex and Claude ANSI screen parsing; source
precedence; transition de-duplication; generated hook settings; failure exit
codes; and unavailable inventory.

## Conclusions

1. **Useful status is derivable.** For the exercised Claude TUI, ATC reliably
   distinguished startup uncertainty, ready/idle, waiting for input, completed,
   failed, and unavailable. `working` and permission mapping are deterministic
   and use the same provider event shapes already proven by the earlier
   provider experiments.
2. **Structured signals are sufficient when their lifecycle coverage is
   complete.** Claude hooks are immediate, correlated by provider session id,
   survive host restarts through persisted evidence, and carry enough detail to
   distinguish an interactive question from a general permission request.
3. **Screen detection is useful but should remain a fallback.** It covered the
   pre-hook ready state and can bootstrap Codex/Claude status from declarative
   rules. It is inherently sensitive to wording and redraw behavior: Claude's
   trust dialog and idle composer both contain `❯`, for example, and require a
   more specific trailing `Enter to confirm` rule. This is acceptable as
   best-effort evidence, not provider truth.
4. **The provider boundary stays small.** The supervisor supplies provider
   kind, current terminal screen, and process evidence. `internal/agentstatus`
   owns launch instrumentation, raw signal storage, provider mappings, screen
   rules, normalization, precedence, and transition history.
5. **One-source precedence works.** While a child runs, recognized structured
   evidence wins over screen detection, which wins over process state. After
   exit, the process marker is final so a stale `working` or `waiting` event
   cannot mask completion or failure.

## Recommendation

Carry this shape into a unified Go-core prototype. Use Claude hooks as its
structured TUI adapter. Add a Codex structured adapter only alongside the
dedicated `codex app-server --listen` / `codex --remote` topology established
by the provider-identity experiments; until then, Codex screen rules should be
labeled best-effort and process fallback should remain `unknown` rather than
inventing confidence.
