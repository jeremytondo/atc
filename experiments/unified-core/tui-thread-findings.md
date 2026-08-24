# ATC-234 TUI thread management findings

Status: deterministic gate and isolated live-provider transition matrix passing

Observed on 2026-08-23 with Codex CLI 0.149.0 and Claude Code 2.1.237. The
live run used the prototype's private provider profiles, a private zmx
namespace, and a loopback-only core. It submitted no model prompts and used no
provider quota.

## Decision

A zmx Terminal and an agent conversation are different resources. One running
TUI can select several durable provider conversations over its lifetime, so a
Terminal cannot be the identity of an ATC Thread and the relationship cannot
remain one-to-one.

Keep one ATC TUI Thread for each exact provider conversation. Let several of
those Threads refer to the same Terminal, and expose the Terminal's current
`activeThreadId`. When the TUI creates an unknown provider conversation, create
one ATC Thread and persist its private provider mapping. When the TUI selects a
known provider conversation, reuse the mapped ATC Thread and move
`activeThreadId`; never duplicate or replace it.

The production identity key must be scoped by the provider runtime/profile in
addition to the provider conversation ID. This prototype has exactly one
private profile per provider, so its disposable store only needs provider plus
conversation ID.

## Structured signals

### Codex

The TUI is itself an app-server writer. Its correlated responses to
`thread/start`, `thread/resume`, and `thread/fork` contain the exact selected
root ID. The per-TUI relay already sees both request and response, which makes
that pair authoritative even when one shared app-server broadcasts events for
many unrelated roots.

The relay now emits a private normalized identity observation for every
successful management response, replaces its active root, clears the previous
root's descendant correlation, and forwards status only for the new root and
its learned descendants. An uncorrelated `thread/started` broadcast cannot
change the Terminal selection.

The installed CLI's generated experimental schema contains all three methods.
The current upstream TUI source also implements `/new`, `/resume`, and `/fork`
as in-process app-server thread transitions rather than process replacement.

### Claude

Claude's official hook contract provides the required identity directly.
Every hook payload carries `session_id`, while `SessionStart` identifies the
authoritative lifecycle transition. Its `source` distinguishes `startup`,
`resume`, `clear`, `compact`, and `fork`. `SessionEnd.reason` independently
reports `clear` and `resume` when the old session is left.

The prototype now installs `SessionStart` alongside its existing status and
`SessionEnd` hooks. A changed `session_id` creates or selects an ATC Thread. A
`compact` event keeps the same ID and is therefore idempotent rather than a
false thread creation. Ordinary hooks may contain an ID but cannot move the
Terminal, which prevents delayed status evidence from selecting a stale
conversation.

Reference: [Claude Code hooks](https://code.claude.com/docs/en/hooks).

## Prototype behavior

Provider-specific extraction remains below the status seam. The core receives
only an exact private identity and a neutral cause, then owns the mapping and
public resource events:

- the launch Thread is bound to the TUI's first exact provider identity;
- the first observation of another identity creates one TUI Thread;
- a repeated identity resolves to the existing Thread, including after core
  restart;
- `terminal.active_thread_changed` identifies the newly selected ATC Thread;
- the public Terminal includes `activeThreadId`, never the provider ID;
- all Threads seen in one TUI retain the same public Terminal association;
- the play client labels each associated Thread as active or inactive; and
- the old Thread becomes `unknown` when the TUI stops observing it instead of
  retaining a misleading idle or working state.

Process exit clears activity for every Thread associated with that Terminal,
not only the Thread that originally launched it.

The deterministic suite covers Codex start, fork, resume, unrelated-broadcast
rejection, exact reuse, active-root filtering, provider-ID privacy, shared
Terminal projection, and reconstruction from the persisted mapping without a
duplicate Thread. Claude fixtures cover startup, in-process resume, and
rejection of ordinary delayed hook events as identity transitions.

## Live acceptance

Codex ran through a test-owned zmx Terminal connected by the passive relay to a
private shared app-server. `/new` produced a different provider root and one
new ATC Thread. `/resume` selected a persisted conversation and moved the same
Terminal to another ATC Thread. After `/new` again, resuming that exact
conversation kept the ATC Thread count unchanged and restored its prior ATC
Thread ID as `activeThreadId`.

Claude ran through a second test-owned zmx Terminal with HTTP hooks. Startup
bound the preassigned session to the launch Thread. `/clear` emitted a new
`SessionStart` ID and created one ATC Thread. `/resume` selected a persisted
session and created one mapping; after another `/clear`, resuming that same
session kept the Thread count unchanged and restored the prior ATC Thread ID as
`activeThreadId`.

Both test-owned zmx sessions were terminated through the canonical API. The
private namespace was empty and both loopback listeners were closed afterward.

## Limits and production changes

- Replace the prototype's `Terminal.threadId` launch-owner compatibility field
  with an explicit creation relationship, or remove it. `activeThreadId` is
  the meaningful live relationship.
- Scope provider identity by the owned provider runtime/profile. Provider name
  plus conversation ID is not a sufficient global production key.
- Persist provider-reported working-directory metadata during discovery.
  Codex management responses and Claude hook payloads expose it; copying the
  launch Thread's cwd, as this prototype does, is insufficient when a user
  resumes a conversation from another directory.
- Define and enforce the competing-writer rule if two Terminals try to select
  the same provider conversation. This run exercised one TUI writer per
  conversation.
- Reconcile inactive status independently when a provider supports it. The
  per-TUI signal can truthfully say only that the old Thread is no longer
  observed, so the prototype reports `unknown`.
- A zero-turn Codex root is observable immediately but did not appear in the
  installed TUI's resume picker. Retain the earlier fail-closed rule: do not
  promise resumability until provider persistence is independently verified.
- Treat the exact event shapes as provider adapter contracts and rerun this
  transition matrix on provider upgrades.

## Conclusion

Both current TUIs expose enough structured evidence to tie their exact
conversations to ATC Threads and to follow in-process creation and switching.
No screen parsing or terminal-output heuristic is needed. The durable model is
provider conversation to ATC Thread, many Threads to one Terminal, and one
explicit active Thread per running TUI.
