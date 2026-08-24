# ATC-230 findings

Tested on 2026-08-22 with Go 1.26.4 and zmx 0.6.0.

## Exercised matrix

| Workload or condition | Result | Evidence |
| --- | --- | --- |
| Login shell | Pass | Created and detached, recovered from a second Go process with the same daemon PID, accepted input, returned scrollback, and stopped cleanly. |
| Long-running process (`sleep 120`) | Pass | Settled under zmx independently of the creator host and was deliberately terminated by a later host. |
| Short process (`true`) | Pass | Its atomic marker recorded exit code 0. zmx briefly reported the dying daemon as unreachable, then inventory absence reconciled to `exited` rather than `missing`. |
| Temporarily unreachable entry | Pass in deterministic test | Reconciles to `disconnected`; cleanup preserves it. |
| Missing session without exit evidence | Pass in deterministic test | Starts as `missing`, becomes `stale` only after the configured grace period. |
| Inventory unavailable | Pass in deterministic test | Managed state becomes `disconnected`, and cleanup refuses to act without a complete inventory. |
| Orphan in the private namespace | Pass in deterministic test | A reachable orphan is reported as stale and removed only by explicit cleanup; an unreachable orphan is preserved. |
| Real authenticated Codex TUI | Pass | Codex CLI 0.149.0 (authenticated with ChatGPT) started read-only with no prompt, rendered its trust screen, detached through zmx, survived two fresh Go host processes with unchanged daemon and child PIDs, reattached, and stopped with exit code 0. |

The standard race-enabled suite covers the recovery decisions using a fake
terminal boundary. The opt-in real-zmx smoke covers PTY-backed creation,
detachment, input, scrollback output, inventory, and verified termination in a
throwaway socket directory.

## Answers to the prototype questions

1. **Can Go create, supervise, and interact with zmx sessions?** Yes for shells
   and ordinary processes. A short-lived Go PTY client creates the session and
   detaches after the daemon is reachable; later processes use zmx inventory,
   `send`, `history`, `attach`, and `kill` through one adapter.
2. **Do sessions survive a Go host restart?** Yes. A fresh compiled host loaded
   the saved metadata, found the same zmx daemon PID, sent input, and read the
   resulting output. No Go process remains resident between commands.
3. **Are the important states distinguishable without brittle heuristics?**
   Broadly yes. Inventory supplies presence, reachability, and daemon identity;
   an explicit child marker supplies exit evidence; persisted stop intent
   supplies deliberate termination. Time is used only to promote an
   evidence-free `missing` record to cleanup-eligible `stale`, never to invent
   a successful exit.
4. **Can zmx stay localized?** Yes. Only `internal/terminal` knows commands,
   environment traps, list formatting, PTY creation, polling, or zmx's
   attach-auto-creates behavior. The supervisor depends on a six-method
   terminal interface.
5. **Does the model work for real agent TUIs?** Yes for the authenticated Codex
   TUI. It exercised the same PTY path without submitting an agent prompt or
   approving a tool: startup output, attach, detach, restart recovery, reattach,
   deliberate stop, structured exit evidence, and cleanup all passed.

## Important behavior

- `zmx attach` auto-creates. Create rejects an existing name, and attach
  pre-flights reachability and compares daemon PID so a race cannot silently
  replace the intended session.
- `zmx kill` returns before the session is necessarily absent. Termination is
  complete only after repeated full inventories prove the name is gone.
- An `err=` inventory row means existing but unreachable. It blocks creation
  and automatic cleanup.
- A fast child can exit while its zmx daemon is still briefly visible as
  unreachable. That transient state is accurately reported as disconnected;
  the completed exit marker becomes authoritative once the daemon disappears.
- The wrapper is intentionally not a second supervisor. It forwards terminal
  stdio and signals to one child and records structured lifecycle evidence;
  zmx remains the process/session owner across host restarts.
