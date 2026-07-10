> **Historical (archived 2026-07):** Describes the pre-monorepo atc-era system. Names, paths, and instructions here are obsolete — see AGENTS.md and docs/platform-policy.md for current structure and policy.

# Connections and Settings Plan

Status: Superseded by `docs/connections-settings-spec.md` (implemented)

## Goal

atc should support multiple named Connections to atc servers while keeping Projects and Terminal Sessions owned by the atc API. The app should show Projects from all configured Connections in one project-first sidebar, make the Connection visible on each Project row, and provide a robust macOS Settings window for Connection management.

## Decisions

- A Connection is local to atc. It has an app-chosen name, API URL, optional token, stable local identity, and creation-order position.
- Projects remain atc-owned records. atc displays each Project through the Connection it came from.
- Terminal Sessions remain atc-owned records created on a particular atc server. atc controls them through their Connection.
- The project sidebar shows all configured Connections together as a simple flat Project list.
- Each Project row shows a Connection chip with the Connection name and a status dot.
- Chip color is not user-customizable and does not identify the Connection. Dot color indicates reachability: gray unknown/loading, green connected, red unreachable.
- The sidebar contains only real Projects. It does not invent rows for broken Connections with no loaded data.
- If a Connection becomes unreachable after Projects were loaded in memory, the app can keep showing those Projects for the running app session while marking the Connection red.
- atc v1 only surfaces project-scoped Terminal Sessions in the app UI. atc API/CLI can continue to support unscoped Terminal Sessions.
- New Project creation exposes a Connection selector. It may preselect the first Connection by creation order, but the selected Connection is always visible and changeable.
- Changing the selected Connection while creating a Project clears the chosen directory but keeps the typed Project name.
- The folder picker is disabled until a Connection is selected because browsing is server-specific.
- Connection names are required but do not need uniqueness enforcement.
- Duplicate Connections to the same host and port are not allowed. Matching ignores scheme and path; URLs must be origin-only with no path.
- User input may infer `http://` when the scheme is omitted, but saved URLs must be explicit `http` or `https`.
- Tokens stay in plain app preferences for now; no Keychain work in this pass.
- There is no default Connection concept. Connections use creation order.
- First launch with no Connections shows an empty state. The app should not seed a hardcoded `Workstation` Connection.
- Settings remains a dedicated macOS Settings window opened through the standard macOS app menu and keyboard shortcut.
- Settings v1 contains Connections only.
- Connection editing uses draft fields and explicit Save/Cancel.
- Test Connection can run against valid draft URL/token values before save. It should call health and version.
- Saving a URL/token change rebuilds that Connection's client, disconnects live terminal attaches for that Connection after confirmation, and refreshes Projects/Sessions.
- Deleting a Connection is local-only. It never deletes Projects or Terminal Sessions from atc. Delete always confirms and explains that server data remains.
- Project archive visibility is a project-list filter control, not an app Settings item.
- Projects with active Terminal Sessions cannot be archived. Active means `starting` or `running`; this should be enforced by atc and mirrored in atc when possible.

## Non-Goals

- No atc API changes for Connection storage.
- No cross-server Project identity.
- No durable offline Project cache.
- No user-configurable Connection colors.
- No Connection reordering in v1.
- No Keychain token storage.
- No in-main-window Settings overlay or Settings sidebar mode.
- No app UI for unscoped Terminal Sessions in v1.
- No hardcoded seeded production Connection.

## Implementation Outline

1. Add local Connection persistence.
   - Replace the single URL/token settings model with a persisted Connection list.
   - Store name, URL string, optional token, stable ID, and creation order.
   - Validate URL shape, origin-only requirement, scheme, host, effective port, and duplicate host+port.

2. Build the Settings window Connections UI.
   - Keep SwiftUI `Settings` as the canonical Settings entry point.
   - Add a Settings layout for managing Connections.
   - Provide Connection list, add, edit, save/cancel, remove, and test actions.

3. Make app state Connection-aware.
   - Replace the single shared client/store assumption with per-Connection client state.
   - Aggregate Projects and project-scoped Terminal Sessions across Connections.
   - Keep app-level selection and terminal attachment scoped by Connection plus server record ID.
   - Rebuild and refresh only the affected Connection after edit/save.

4. Update the project sidebar.
   - Render one flat list of real Projects from all loaded Connections.
   - Add a Connection chip to every Project row.
   - Remove `Other Sessions` from the atc UI.
   - Move archived Project visibility behind a compact project-list filter/menu.
   - Disable project archive when known active sessions exist.

5. Update creation flows.
   - Add a Connection selector to New Project.
   - Route folder browsing and Project creation through the selected Connection.
   - Clear the chosen directory when the selected Connection changes.
   - Route New Session creation, action loading, and terminal attach through the Project's Connection.

6. Add tests.
   - Connection validation and persistence tests.
   - Duplicate host+port tests, including scheme differences and trailing slash normalization.
   - Aggregation and selection identity tests.
   - Sidebar grouping tests without `Other Sessions`.
   - New Project Connection selector behavior tests.
   - Settings hosting smoke test.

## Open Implementation Notes

- Use the simplest persisted preferences format that fits the existing app, likely encoded Connection records in app preferences.
- Keep stale async test/poll results harmless if a Connection is edited or removed while work is in flight.
- Do not add sorting policy beyond the natural stored/loaded list order unless the UI proves confusing.
