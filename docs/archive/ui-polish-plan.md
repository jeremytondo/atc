# macOS UI Polish Plan — Design System, Spacing, Liquid Glass

Status: proposed
Date: 2026-07-13

## Context

The macOS app (`macos/ATC`) looks unpolished: spacing is ad-hoc everywhere, similar
components are hand-rolled multiple times with drifting styles, and despite targeting
**macOS 26** the app uses zero Liquid Glass APIs. Audit findings:

- **No design tokens exist.** 14 distinct `spacing:` values (1–34), 16 padding forms
  (horizontal: 5, 6, 7, 8, 12, 14, 18, 36), 4 corner radii (4, 5, 6, 12), split-brain
  dimming (0.5 vs 0.55 for the same "archived" intent), 9 bespoke sheet/window sizes.
- **Duplication:** reachability dots hand-rolled in 5 places at 4 sizes; 4+ pill/badge
  implementations mixing Capsule vs RoundedRectangle; the "Action Failed" `.alert`
  block copy-pasted 5×; identical +/− settings list bars duplicated in two files.
- **Dashboard uses its own visual language** — oversized custom scale (spacing 34,
  padding 32/36) unlike the rest of the app.
- **Hand-rolled controls fight the framework:** the sidebar navigator selector is plain
  Buttons with manual accent fills and a hardcoded 13pt font
  (`NavigatorSidebar.swift:29-57`).

**Decisions (fixed):** stay dark-only (`.preferredColorScheme(.dark)` stays); keep
dashboard cards but polish them; standard (non-expressive) Liquid Glass adoption.
Xcode 26 is the visual reference; prefer default SwiftUI components; minimal clutter.

## Phase 0 — Design token layer

New file `macos/ATC/Shared/DesignTokens.swift` — three tiny caseless enums, no
theming framework:

```swift
enum Spacing {  // 4pt grid
    static let xs: CGFloat = 4    // intra-label gaps (dot–text)
    static let sm: CGFloat = 8    // control clusters, row internals
    static let md: CGFloat = 12   // standard container/bar padding
    static let lg: CGFloat = 16   // card interior padding
    static let xxl: CGFloat = 32  // dashboard page margins / section gaps
}
enum Radius {
    static let control: CGFloat = 6
    static let card: CGFloat = 12
}
enum Dimming {
    static let archived: Double = 0.5   // also replaces the stray 0.55
}
```

Mapping rules (mechanical, by intent not by old number):

- 3, 4, 6, 7 gaps → `xs` (dot–text) or `sm` (element gaps); keep literal `2` for tight
  two-line text stacks
- 8, 9, 10 → `sm`; 12, 14 → `md`; 18 → `lg`; 32/34/36 → `xxl`
- radius 4/5/6 → `Radius.control` (the 5pt badge becomes a Capsule via TagBadge);
  12 → `Radius.card`
- `.font(.system(size: 13, weight: .medium))` at `NavigatorSidebar.swift:38` → dies
  with Phase 2a
- **Keep** the Catppuccin Mocha RGB in `TerminalPane.swift:13` (pairs with the Ghostty
  surface, not app chrome) — add a comment saying so

Files touched: DashboardView, NavigatorSidebar, SessionContentView,
ConnectionsSettingsView, ActionsSettingsView, the three create/start sheets,
RemoteFolderPickerSheet, TerminalPane, WorkspaceShellView, SessionRowView,
ConnectionChip, StatusBadge.

## Phase 1 — Shared components (all in `macos/ATC/Shared/`)

1. **`StatusDot.swift`** — the one reachability/liveness dot. Two sizes (inline 6pt /
   standard 8pt), optional `hollow` (dashboard "no active sessions" ring). Compose into
   existing `StatusBadge` and `ConnectionChip`; adopt at
   `ConnectionsSettingsView.swift:61`, `DashboardView.swift:170-173` (**drop the glow
   shadow** — only glow in the app, off-idiom), `DashboardView.swift:394-401`,
   `NavigatorSidebar.swift:218`, `WorkspaceSwitcher.swift:54`. Also move
   `Features/Sessions/StatusBadge.swift` → `Shared/`.
2. **`TagBadge.swift`** — the one text pill: `.caption2`, secondary, `.quaternary`
   Capsule, padding (6, 2). Replaces the LOCAL/REMOTE badge
   (`DashboardView.swift:178-183`, gets `monospaced: true`), both Archived pills
   (`DashboardView.swift:274-279`, `407-413`), the action-label pill
   (`SessionContentView.swift:152-157`), and `ActionOriginBadge`
   (`ActionsSettingsView.swift:263-282`, delete the struct).
3. **`ErrorAlert.swift`** — `func actionErrorAlert(_ error: Binding<String?>, title:
   String = "Action Failed")` view extension. Replaces the 5 copy-pasted alert blocks:
   `DashboardView.swift:154`, `NavigatorSidebar.swift:202`, `WorkspaceShellView.swift:76`
   & `:314`, `SessionContentView.swift:235` (custom titles preserved via param). Leave
   the per-view `run{}` helpers alone — they genuinely differ.
4. **`SheetScaffold.swift`** — standard form-sheet chrome: `Label` title header
   (headline) + Divider, grouped `Form` content, Divider, trailing button row
   (`Spacer / Cancel / Primary`) with `.cancelAction`/`.defaultAction` shortcuts and an
   `isBusy` spinner state. **Button order: Cancel adjacent-left of primary, both
   trailing** — the HIG/Xcode-correct arrangement (the folder picker already does this;
   the three form sheets change to match).
5. **`ListEditorBar.swift`** — the +/− bar under settings master lists; replaces
   `ConnectionsSettingsView.swift:80-100` and `ActionsSettingsView.swift:160-181`
   verbatim.

## Phase 2 — Per-surface fixes

- **2a. Navigator selector → native control** (`NavigatorSidebar.swift:29-57`): replace
  the hand-rolled buttons with `Picker` + `.pickerStyle(.palette)`, `.labelsHidden()`,
  icon items tagged by `NavigatorID`, `.selectionDisabled(!option.isEnabled)` + `.help`
  per item. Placement stays at the top of the sidebar (matches Xcode's navigator bar).
  Keep `NavigatorSelectorOption` unchanged — `NavigationPresentationTests` asserts on
  it. *Fallback if `.selectionDisabled` doesn't gate palette items on macOS 26:* row of
  `.buttonStyle(.accessoryBar)` buttons with per-item `.disabled`.
- **2b. Dashboard** (`DashboardView.swift`): token pass (34→xxl etc.), `StatusDot`
  (no glow), `TagBadge`, hollow ring → `StatusDot(hollow:)`, dashed empty box radius →
  `Radius.card`, `maxWidth: 1280` stays, alert → `actionErrorAlert`.
- **2c. SessionHeaderBar** (`SessionContentView.swift:103-263`): pill → `TagBadge`;
  padding → md/sm; apply `.buttonStyle(.borderless)` + `.labelStyle(.iconOnly)` at the
  HStack level so the action buttons read as a toolbar row, keep `.help` strings;
  alert → `actionErrorAlert(title: "Session Action Failed")`.
- **2d. Settings**: one window size 720×500 for both tabs (constant in
  `SettingsView.swift`); master column width unified to 240; both bottom bars →
  `ListEditorBar`; inline spinners standardize on `.controlSize(.small)` (pane-level
  loader stays regular). Update `SettingsHostingSmokeTest` frames to match.
- **2e. Sheets**: CreateProjectSheet → scaffold "New Project" 460×300;
  CreateWorkspaceSheet → "New Workspace" 460×280; StartWorkspaceSessionSheet → title
  moves from the Form-section hack into the scaffold header, 460×280.
  RemoteFolderPickerSheet is a browser, not a form — keep custom body, just token pass
  + same footer padding (its trailing button pair is already correct).
- **2f. Terminal status banner** (`TerminalPane.swift:77-84`):
  `.background(.regularMaterial, in: Capsule())` → `.glassEffect()` with token padding.

## Phase 3 — Liquid Glass boundaries

- `glassEffect()` **only** on the terminal status banner (the app's one true floating
  overlay). No tint, no `interactive()`, no `GlassEffectContainer`, no
  `.buttonStyle(.glass)`.
- Everything else gets glass for free from system components (sidebar, toolbar,
  inspector, sheets, menus) — the codebase sets no
  `toolbarBackground`/`scrollContentBackground` overrides, so nothing to remove. The 2a
  native Picker is the main "use the system control, get glass" win.
- No glass on dashboard cards or session header (HIG: glass is the control layer, not
  content).

## Sequencing — jj checkpoints (each buildable, tests green; `jj describe -m` + `jj new`)

1. `design: add DesignTokens, apply spacing/radius/dimming scale` (Phase 0 —
   mechanical, zero behavior change)
2. `design: add StatusDot + TagBadge, adopt across dots and pills`
3. `design: shared actionErrorAlert modifier`
4. `design: native palette Picker for navigator selector` (isolated — contained revert
   if fallback needed)
5. `design: SheetScaffold + unify create/start sheets, folder picker polish`
6. `design: settings — unified frame, master width, ListEditorBar`
7. `design: dashboard + session header polish`
8. `design: liquid glass terminal status banner`

## Verification

Per checkpoint:

- Build: `xcodebuild -project macos/atc.xcodeproj -scheme atc -destination
  'platform=macOS' build`
- Tests: same command with `test` — the four hosting smoke suites
  (Settings/ProjectUI/Picker/Shell) are the regression net for checkpoints 4–7
- Visual: run the app; walk Dashboard → open workspace → new session sheet → both
  settings tabs → folder picker; toggle sidebar navigators; kill the server to check
  the glass reconnect banner. Verify each sheet with its conditional error section
  visible (heights can clip). Previews exist for all touched surfaces.

## Risks

- `NavigationPresentationTests.swift` asserts `NavigatorSelectorOption.all`
  ordering/gating/help strings and `WorkspaceSwitcherPresentation` labels — keep
  symbols and semantics intact.
- `.selectionDisabled` on palette Picker items unverified on macOS 26 — fallback
  documented in 2a.
- Sheet heights with grouped Forms need per-sheet eyeballing with error states visible.
- Do not touch the Ghostty terminal background RGB.
