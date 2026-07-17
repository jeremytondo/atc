# Command Palette Navigation Implementation Spec

Status: Draft v1 — reconciled against the current `macos/` sources

Scope: the navigation extension described in
[command-palette-navigation-brief.md](command-palette-navigation-brief.md) —
the existing `Shift-Cmd-P` palette gains Workspace results (app-wide) and
Session & Terminal results (Active Workspace only) alongside the current
command list. All work lands in `macos/`; no server, ATCKit, or
GhosttyTerminal package change is required. The palette's opener, router
suspension, overlay placement, and dismissal machinery from
[command-palette-spec.md](command-palette-spec.md) are untouched.

Related: [command-palette-spec.md](command-palette-spec.md) (the palette this
extends — §5 presentation, §6 interaction, and §7 routing all still apply
except where amended here), [keyboard-shortcuts-spec.md](keyboard-shortcuts-spec.md)
(registry and router), `macos/CONTEXT.md` (terminology).

## 1. Module Layout

No new files. Changed files:

- `Features/CommandPalette/CommandPaletteContent.swift` — grows from the
  command-row projection into the full result projection: the heterogeneous
  result model, Workspace and Session candidate builders, per-bucket
  ordering. Stays a pure `@MainActor` projection the view calls from `body`.
- `Features/CommandPalette/CommandPaletteView.swift` — heterogeneous row
  rendering, selection by result identity, updated copy, navigation
  activation, and the navigation-dismissal focus rule.

Unchanged and deliberately reused, not duplicated:

- `Features/CommandPalette/QueryMatcher.swift` — the one matcher, applied
  per-field to navigation titles exactly as it is to command titles.
- `Features/Projects/ProjectsNavigatorGroups.swift` — the shared
  Project/Workspace projection (already excludes archived Projects and
  Workspaces) becomes the palette's Workspace candidate source, making the
  palette agree with the Projects Navigator and Workspace Switcher by
  construction.
- `Features/Sessions/SessionKind.swift` — `displayName` and `classify` are
  the display-name and Session/Terminal-label rules, shared with the
  Workspace Navigator.
- `WindowState.activateWorkspace` / `WindowState.selectSession` — the only
  navigation transitions; the palette adds no selection, attachment,
  restoration, or validation logic of its own.
- `WindowKeyboardRouter.showUnavailable(reason:)` — the existing
  unavailable-feedback path, reused for unreachable Workspace results.

## 2. Result Model

One flat list of heterogeneous results with stable, connection-qualified
identity. Selection, `ForEach`, and scroll anchoring key off
`PaletteResultID` (today they key off `CommandID`):

```swift
enum PaletteResultID: Hashable {
    case command(CommandID)
    case workspace(WorkspaceRef)
    case session(SessionRef)
}

enum PaletteResult: Identifiable {
    case command(CommandPaletteRow)
    case workspace(WorkspaceResult)
    case session(SessionResult)

    var id: PaletteResultID
}
```

`CommandPaletteRow` is unchanged. The two new row models carry exactly what
their rows render — no status, previews, or current-target indicators:

```swift
struct WorkspaceResult: Identifiable {
    let ref: WorkspaceRef
    let title: String                          // Workspace name
    let projectName: String
    let connectionName: String                 // "Project · Connection" context
    let matchedRanges: [Range<String.Index>]   // within title only (§3)
    let availability: CommandAvailability      // reachability gate (§3)
    var id: PaletteResultID { .workspace(ref) }
}

struct SessionResult: Identifiable {
    let ref: SessionRef
    let title: String                          // SessionKind.displayName
    let kind: SessionKind                      // "Session" / "Terminal" label
    let matchedRanges: [Range<String.Index>]
    var id: PaletteResultID { .session(ref) }
}
```

Reusing `CommandAvailability` for the Workspace reachability gate keeps one
unavailable representation (dimmed row, inline reason, `showUnavailable`
flash) across every result type. Session results carry no availability —
selection is local navigation and is never gated on reachability.

## 3. Result Projection (`CommandPaletteContent`)

The entry point replaces `rows(query:keymap:context:)`:

```swift
@MainActor
enum CommandPaletteContent {
    static func results(
        query: String,
        keymap: ResolvedKeymap,
        context: CommandContext
    ) -> [PaletteResult]

    // Pure helpers, unit-testable without AppModel:
    static func workspaceResults(
        query: String,
        groups: ProjectsNavigatorGroups
    ) -> [WorkspaceResult]

    static func sessionResults(
        query: String,
        activeWorkspace: WorkspaceRef,
        sessions: [Session],
        actions: [ATCAction]
    ) -> [SessionResult]
}
```

Everything is computed synchronously from already-loaded in-memory stores on
every call — no API requests, attachment, caching, debounce, or async state.
The view keeps calling the projection from `body`, so store polling,
reachability changes, and keymap reloads update the open palette through
plain SwiftUI observation.

### Bucket order

For a query whose trimmed form is empty, the result is the existing eligible
command list and nothing else. For a nonempty trimmed query, the list is,
deterministically:

1. **Commands** — the existing projection, unchanged (eligibility, matching,
   alphabetical order with `CommandID` tie-break, `menuShortcuts`,
   availability).
2. **Workspaces** — sorted by lowercased title (plain Unicode comparison,
   matching the command bucket), tie-broken by `connectionID.uuidString`,
   then `workspaceID`.
3. **Sessions & Terminals** — one combined bucket sorted by lowercased
   title, tie-broken by `sessionID`.

No relevance scoring, no cross-bucket interleaving, no result cap. Rendering
already uses `LazyVStack`; the complete matching set is returned.

### Workspace candidates

Built from `ProjectsNavigatorGroups(runtimes: appModel.runtimes)`, flattening
every `ProjectGroup`'s `WorkspaceRow`s. Because the shared projection already
drops archived Projects (and their Workspaces) and archived Workspaces, the
palette inherits the Navigator/Switcher exclusion rules exactly; the palette
adds no archive filtering of its own for Workspaces.

- **Matching**: apply `QueryMatcher` to the Workspace name, the Project name,
  and the Connection name independently; the complete query must match at
  least one field. No cross-field token combinations. `matchedRanges` carries
  the Workspace-name ranges when that field matched, and is empty when only
  the Project or Connection name matched — highlighting is supplemental and
  only the primary label is highlighted (Resolved Decision 2).
- **Availability**: `group.reachability == .connected` → `.available`;
  otherwise `.unavailable(reason: "Requires a reachable Connection")`. This
  is the same `!= .connected` rule the Workspace Switcher and Projects
  Navigator use to disable rows, so `.unknown` (no refresh outcome yet) is
  unavailable too (Resolved Decision 1).
- The Active Workspace, when it matches, appears as an ordinary result — no
  checkmark, no special presentation. Activating it is handled by
  `activateWorkspace`'s existing idempotence (a plain no-op unless the window
  is on the Dashboard, from which it returns to the remembered content).

### Session & Terminal candidates

Only when `windowState.activeWorkspace` is non-nil and its runtime exists;
otherwise the bucket is empty with no informational row. From
`runtime.sessions.sessions`:

- Keep sessions where `session.belongs(to: activeWorkspace)` and
  `!session.isArchived`. Archive filtering is per target: an archived Active
  Workspace still contributes its unarchived Sessions and Terminals even
  though the Workspace itself is absent from the Workspace bucket. The
  Workspace Navigator's local `Show Archived` toggle is view-local `@State`
  and is ignored by design.
- `title` is `SessionKind.displayName(session:actions:)`; `kind` is
  `SessionKind.classify(session:actions:)` with
  `runtime.actions.actions` — the same rules as the Workspace Navigator.
- **Matching**: `QueryMatcher` against `title` only. Hidden Action names,
  lifecycle status, and raw identifiers never match.
- Reachability does not gate these rows: already-loaded results stay
  selectable when the Connection is unreachable; the terminal surface owns
  disconnected and recovery state.

## 4. Presentation

The overlay, panel chrome, layout metrics, and list behavior from
command-palette-spec §5 are unchanged. Amendments:

### Copy

- Query field placeholder: `Search commands and navigation…` (was
  `Execute a command…`).
- Query field accessibility label: `Palette search` (was `Command query`).
- Empty state and its VoiceOver announcement: `No matching results` (was
  `No matching commands`). Shown only when the entire filtered list is
  empty — there is no per-bucket empty row.

### Rows

One flat list, no section headers; result types are identified per row:

- **Command rows**: unchanged — highlighted title, trailing shortcut glyphs,
  dimmed-with-reason when unavailable.
- **Workspace rows**: the Workspace name as the primary label (with
  supplemental match highlighting), and `Project · Connection` as a
  secondary caption line rendered on every Workspace row, even when names
  are unique. Unavailable rows reuse the command treatment: dimmed, with the
  reason inline in tertiary text.
- **Session/Terminal rows**: the display name as the primary label (with
  highlighting) and a small trailing `Session` or `Terminal` text label in
  the caption style the shortcut glyphs use — explicit text, not a type
  icon.

The row-height estimate feeding the list's max-height calculation stays a
single approximation; two-line Workspace rows are close enough to the
existing 42 pt figure that the ~200 pt cap and shrink-to-fit behavior are
unchanged (verified visually in build step 3).

## 5. Interaction and Activation

Typing filters, arrows and `Control-N`/`Control-P` move selection with
wrapping, Return activates, Escape dismisses, hover highlights without
moving selection, outside click dismisses — all unchanged, now over
`PaletteResultID`. Selection still resets on every query change: first row
of a nonempty query's results, none for an empty query.

Activation is one code path for Return and click, per result type:

| Result | Behavior |
|---|---|
| Command, available | Unchanged: dismiss, then `CommandRegistry.execute` |
| Command, unavailable | Unchanged: stay open, `router.showUnavailable(reason:)` |
| Workspace, available | Dismiss (navigation rule below), then `windowState.activateWorkspace(ref, in: appModel)` |
| Workspace, unavailable | Stay open; `router.showUnavailable(reason: "Requires a reachable Connection")` |
| Session / Terminal | Dismiss (navigation rule below), then `windowState.selectSession(ref, in: appModel)` |

Every selection is one-shot: the palette dismisses before navigation runs,
in the same MainActor turn. Navigating within a newly activated Workspace
means reopening the palette. Both `WindowState` transitions validate their
target and fail closed; a `false` return (the target vanished between
projection and activation) leaves the window unchanged with no error UI —
the same silent rejection every other navigation surface gets (Resolved
Decision 4).

### Focus on navigation dismissal

Command activations keep the existing dismissal behavior: the palette's
window accessor restores the pre-palette first responder (or falls back to
`requestTerminalFocus()`).

Navigation activations must instead land focus on the *selected* target, not
the pre-palette responder: retained terminal views stay mounted (and can
still accept first responder) after a switch, so restoring the captured
responder would fight `selectSession`'s `requestTerminalFocus()` and could
transiently focus the previous terminal. Rule: before dismissing for a
Workspace, Session, or Terminal activation, the view disables
previous-responder restoration; the accessor's `restore()` then skips the
captured responder and invokes only the existing fallback
(`windowState.requestTerminalFocus()` — a no-op when no Terminal Session is
selected, e.g. after activating a Workspace that restores to empty content).

Mechanism: the flag is a shared reference box (`final class` holding one
Boolean) created as view `@State` and read by the coordinator through its
existing closure pattern — a reference type because `dismantleNSView` can
run in the same transaction as the dismissing state change, before any
`updateNSView` would re-copy closure captures.

## 6. Accessibility

Existing behavior (modal container, focused query field, selection
announcements via `AccessibilityNotification`) is preserved. The
per-row spoken label extends to the new types:

- Workspace: `<Workspace name>, Workspace, <Project name>, <Connection
  name>` plus `Unavailable — <reason>` when unreachable.
- Session/Terminal: `<display name>, Session` or `<display name>, Terminal`.
- Command: unchanged (title, availability, spoken shortcut).

The container label stays `Command Palette`; the field label becomes
`Palette search` (§4).

## 7. Test Plan

All in `macos/ATCTests` (Swift Testing). The pure candidate builders take
plain inputs (`ProjectsNavigatorGroups.Input` arrays, `[Session]`,
`[ATCAction]`), so the navigation tests need no async store loading; the
existing `CommandPaletteContentTests` fixture keeps covering the
context-reading entry point.

**CommandPaletteContentTests (reworked)** — existing command assertions
migrate to `results(query:keymap:context:)` unchanged in substance, plus:

- An empty or whitespace-only query returns exactly the eligible command
  list even when Workspace and Session candidates exist.
- A nonempty query returns bucket order Commands → Workspaces → Sessions &
  Terminals, each bucket alphabetically ordered with its documented
  tie-breaks; equal titles order deterministically.
- With no Active Workspace, no Session candidates appear and the all-empty
  query state is the single generic empty result.

**Workspace candidate tests** — built on `ProjectsNavigatorGroups(inputs:)`:

- Matches on Workspace name, Project name, and Connection name
  independently; the complete query matching no single field excludes the
  row (no cross-field combination: a query spanning Workspace + Project
  words matches nothing).
- `matchedRanges` populated only for Workspace-name matches.
- Archived Workspaces and Workspaces under archived Projects are absent;
  Workspaces from every configured Connection are present.
- `reachability == .connected` → available; `.unreachable` and `.unknown` →
  unavailable with the documented reason.
- The Active Workspace appears as an ordinary row.

**Session candidate tests**:

- Only the Active Workspace's Sessions appear; other Workspaces' and other
  Connections' Sessions never do.
- Archived Sessions are always excluded; an archived Active Workspace still
  yields its unarchived Sessions.
- Display names follow `SessionKind.displayName` (user-named session; unnamed
  shell → `Terminal`; unnamed Action session → Action label) and `kind`
  follows `classify`.
- A query matching a Session's hidden Action name, status, or raw id — but
  not its display name — matches nothing.

**Scale test** — a synthetic candidate set (e.g. 50 Connections' worth of
inputs totaling ~2,000 Workspaces and ~2,000 Sessions) projects and filters
correctly with the complete uncapped result set, so truncation can never
hide a performance regression. Correctness only; no wall-clock assertion.

**CommandPaletteHostingSmokeTest (extended)** — hosts the palette against a
seeded `AppModel.preview()` with an Active Workspace so heterogeneous rows
render in the hosting environment.

**Manual checks** (build step 3 exit list):

- Type a Workspace name from another Project: the row shows
  `Project · Connection`; Return activates it, the palette closes first,
  and the remembered content (or empty state) restores.
- From the Dashboard, select the Active Workspace by name: the window
  returns to its remembered content.
- Select a Terminal by display name: the palette closes and keyboard focus
  lands in that terminal, including when a different terminal was focused
  when the palette opened.
- Re-select the currently selected Session: dismisses and refocuses its
  terminal.
- Stop a Connection: its Workspaces render dimmed with the reason; Return
  keeps the palette open and flashes the reason; the Active Workspace's
  Sessions remain selectable and selecting one shows the terminal's own
  disconnected surface.
- Archive the Active Workspace: it leaves Workspace results while its
  Sessions remain; the Workspace Navigator's `Show Archived` toggle changes
  nothing in the palette.
- Empty query shows only commands; one keystroke brings in navigation
  results; clearing the query returns to commands only.
- No Active Workspace: Session results are absent without any placeholder
  row; a query matching nothing shows `No matching results`.
- VoiceOver announces Workspace rows with Project and Connection context and
  Session/Terminal rows with their type label; typing latency stays
  imperceptible with the scale fixture loaded via a mock Connection.

## 8. Build Order

Each step lands green (`mise run macos:test`) with a `jj` checkpoint.

1. **Result model and projection** — `PaletteResultID`, `PaletteResult`,
   `WorkspaceResult`, `SessionResult`, the candidate builders, bucket
   ordering, and the reworked `results` entry point, with the full
   projection test suite (including the scale test). The view still
   compiles against commands via the new API; no visual change beyond
   copy-neutral plumbing.
2. **Rendering and copy** — heterogeneous rows, selection by
   `PaletteResultID`, placeholder / empty-state / accessibility copy,
   extended spoken labels, extended hosting smoke test.
3. **Navigation activation** — the activation table, the
   navigation-dismissal focus rule with its reference-box mechanism, and
   the manual checklist.

## Resolved Decisions

Differences from and clarifications to the brief, recorded per its
instruction; the brief has been updated to match where it conflicted.

1. **Unavailability is `reachability != .connected`, not only
   `.unreachable`.** A Connection that has not completed its first refresh
   (`.unknown`) is also unavailable for Workspace activation, because that
   is exactly the rule the Workspace Switcher and Projects Navigator already
   apply to disable rows — the palette must not be the one surface that
   activates a Workspace the Switcher would refuse. Reason string:
   `Requires a reachable Connection`, matching the registry's existing
   reason style. The brief's "unreachable" wording was updated.
2. **Match highlighting only decorates the Workspace-name label.** A match
   on the Project or Connection name includes the row but produces no
   highlight ranges. Highlighting is supplemental by the existing spec;
   threading ranges into the composed `Project · Connection` caption adds
   index bookkeeping for no navigation value. The brief was silent.
3. **Navigation activations skip previous-responder restoration.** The brief
   requires focus to land in the selected target's terminal; this spec makes
   that concrete: command dismissals keep the capture-and-restore behavior,
   navigation dismissals suppress it and rely on the selection path
   (`selectSession`'s focus request / the workspace activation's restored
   content), with `requestTerminalFocus()` as the unchanged fallback.
4. **A navigation target that disappears between projection and activation
   is rejected silently.** `activateWorkspace`/`selectSession` already fail
   closed; the palette has dismissed by then and adds no error UI. Store
   polling makes the window microscopic, and every other surface behaves
   the same way. The brief was silent.
5. **"Alphabetical" is the command bucket's plain lowercased Unicode
   comparison,** not the localized comparison `ProjectsNavigatorGroups`
   uses for Project groups — one ordering rule inside the palette beats two,
   and it keeps navigation ordering byte-for-byte deterministic like the
   command bucket. The brief was silent on the comparison.
