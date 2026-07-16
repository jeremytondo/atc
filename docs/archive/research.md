# Research

## Codebase Foundation Review - 2026-07-09

Context:

This review is a checkpoint on the whole atc monorepo before more
features are built. The standard is the repository's stated priority order:
performance, reliability, simplicity, and user experience, with correctness,
robustness, readability, and long-term maintainability treated as foundation
requirements.

The review covered the tracked Go server and CLI, SQLite persistence, zmx
boundary, HTTP and WebSocket API, Svelte admin UI, Swift API package, SwiftUI
macOS app, terminal bridge, tests, build/release scripts, GitHub Actions, and
active documentation. The production source inspected is approximately 15,900
lines; production plus tests is approximately 26,000 lines, excluding generated
output and dependencies.

Severity used below:

- **P0 — blocking:** data loss, critical security exposure, or an unusable build.
- **P1 — fix before the next feature wave:** a foundational reliability,
  security, or maintainability gap that becomes more expensive as the product
  grows.
- **P2 — schedule soon:** a real defect or structural weakness with contained
  current impact.
- **P3 — monitor/clean up:** worthwhile hygiene or an explicit policy decision,
  but not a reason to stop current work.

Findings:

### Executive assessment

The codebase is a promising foundation, but it is not yet at a clean foundation
checkpoint. There is no need for a broad rewrite. The core architecture is
mostly simple and well-factored; the highest-value work is to close the quality,
security, and contract gaps around it.

There are no P0 findings. Five areas should be treated as P1 before substantial
new feature work:

1. Establish CI and static-quality gates for every shipped surface.
2. Make remote server exposure fail closed instead of warning and continuing.
3. Bound HTTP request bodies and request lifetimes.
4. Move native connection tokens out of UserDefaults and into Keychain.
5. Establish lightweight cross-client API contract tests.

### What is already strong

- The monorepo shape is direct and understandable: `server/`, `macos/`,
  `packages/`, `docs/`, and `scripts/` expose the product boundaries without an
  unnecessary orchestration layer.
- The Go server has good boundaries. `internal/api` owns wire concerns,
  `internal/session` and `internal/project` own domain behavior, `internal/store`
  owns persistence, and `internal/zmx` contains the external multiplexer seam.
  The session layer depends on narrow interfaces rather than concrete process
  or project implementations.
- Process launching avoids shell interpolation. Actions resolve to argv and zmx
  receives explicit arguments or stdin bytes, which is simpler and safer than
  assembling commands from request strings.
- SQLite is used conservatively: one open connection, foreign keys, busy timeout,
  schema constraints, transactions around cross-record invariants, stable UTC
  timestamp formatting, and embedded migrations.
- The action overlay store uses a temporary file plus rename, validates the
  merged registry before use, and keeps built-in behavior separate from user
  overlays.
- The terminal attach path includes origin checking, frame limits, an initial
  resize protocol, bounded server-to-client writes, deterministic zmx identity,
  and explicit PTY cleanup.
- The Swift app uses connection-qualified project/session identities, a protocol
  API boundary, per-connection runtimes, pure grouping logic, and injectable
  clients. Those choices directly prevent cross-server identity bugs and make
  the 104 app tests useful rather than superficial.
- The Swift API package is small, dependency-free, Sendable-oriented, and tested
  against realistic wire shapes. Unknown filesystem entry kinds degrade safely.
- Distribution work is more robust than typical at this stage: server archives
  have checksums, and the macOS developer-release script validates signing,
  notarization, stapling, and Gatekeeper assessment.
- The repository has invested in ADRs, product terminology, and decision docs.
  That discipline is worth preserving after the naming cleanup described below.

### Validation results

| Check | Result | Notes |
| --- | --- | --- |
| `jj status` | Pass | Working copy was clean before the review document. |
| `go vet ./...` | Pass | No diagnostics. |
| `mise run test` in `server/` | **Fail** | One genuine failure remains on the default macOS filesystem: `TestListSorting` tries to create both `AB` and `ab`, so the expected entry is absent on a case-insensitive volume. Network/socket failures seen in the restricted runner disappeared outside the sandbox. |
| `gofmt -l .` in `server/` | **Fail** | `internal/api/sessions.go` and `internal/zmx/zmx.go` are not gofmt-clean. |
| `CI=true mise run build` in `server/` | Pass with warnings | Web assets were built and embedded; `dist/atc` compiled. Svelte emitted two stale-prop warnings and Vite reported a 638.88 kB terminal-route chunk. |
| `pnpm exec tsc --noEmit` | **Fail** | TypeScript cannot start because the generated SvelteKit config requests Node types but `@types/node` is not installed. |
| `swift test` in `packages/ATCKit` | Pass | 36 tests in 11 suites. |
| `xcodebuild test` for the macOS app | Pass with runtime warnings | 104 tests in 12 suites. Test hosting emitted invalid optional Picker-selection warnings and an `NSTableView` reentrant-operation warning that says it will become an assertion in the future. |
| Shell static analysis | Not run | `shellcheck` is not installed in the current toolchain. |

The important conclusion is not that the repository is unhealthy. It is that
the current CI would report green while several locally reproducible checks are
red or noisy.

### P1-01 — CI covers only the Linux server path

Evidence:

- `.github/workflows/server-ci.yml` runs only when `server/**` changes and only on
  Ubuntu.
- There is no workflow for `macos/**`, `packages/**`, repo-level scripts, or root
  docs/configuration.
- The server workflow runs Go tests and the production build, but it does not
  enforce gofmt, `go vet`, frontend type checking, Svelte diagnostics, or
  frontend tests.
- The root README says each surface builds and tests independently, which is not
  yet true in automation.
- The gaps are observable now: two Go files are unformatted, the Go suite fails
  on macOS, frontend type checking cannot run, and the production web build
  emits compiler warnings while CI would still pass.

Recommendation:

- Add a server job that runs gofmt-check, `go vet`, Go tests, frontend
  type/Svelte checks, frontend tests, and the embedded production build.
- Add a macOS job on a pinned macOS/Xcode image that runs `swift test` and
  `xcodebuild test` with signing disabled.
- Give workflows timeouts and concurrency cancellation.
- Keep path filters, but include shared packages and workflow/build files in all
  affected jobs.
- Make warning policy explicit. At minimum, Svelte compiler warnings and Swift
  runtime warnings found by tests should have tracked owners rather than being
  accepted silently.

This is consistent with both named reference projects. T3 Code exposes separate
`typecheck`, `lint`, `test`, and `fmt:check` commands and runs check, typecheck,
tests, and a desktop pipeline in CI ([package scripts](https://raw.githubusercontent.com/pingdotgg/t3code/main/package.json),
[workflow](https://raw.githubusercontent.com/pingdotgg/t3code/main/.github/workflows/ci.yml)).
AGTerm uses a macOS workflow for Swift tests with coverage, strict SwiftLint,
and a release build ([workflow](https://raw.githubusercontent.com/umputun/agterm/master/.github/workflows/ci.yml)).
The exact tools need not be copied; the useful pattern is that every shipped
surface has an automated executable contract.

### P1-02 — A remote unauthenticated bind fails open, and the warning is wrong when auth is present

Evidence:

- `server/internal/server/server.go:54-56` logs
  `explicit non-loopback TCP bind configured without TCP authentication` for
  every explicit non-loopback bind, without checking `cfg.AuthToken`.
- The service continues starting when the token is empty.
- The exposed API can browse readable host paths, launch configured commands,
  inject terminal input, terminate sessions, and attach to PTYs. This is not a
  harmless diagnostics service.
- The root POC document explicitly describes unauthenticated tailnet use, but the
  runtime cannot distinguish a deliberate tailnet-only bind from `0.0.0.0` on an
  untrusted LAN.

Recommendation:

- Fail startup when the TCP bind is non-loopback and no token is configured.
- If an unauthenticated trusted-network mode remains necessary, require an
  unmistakable explicit setting such as `allow_unauthenticated_remote = true`;
  do not infer trust from a non-loopback address.
- Only emit the unauthenticated warning when the token is actually empty.
- Add tests for loopback/no-token, remote/token, remote/no-token, and explicit
  insecure override.
- Keep the owner-only Unix socket trusted as it is today.

### P1-03 — HTTP JSON requests have no size limit or body deadline

Evidence:

- `server/internal/server/server.go:132-136` configures only a five-second
  `ReadHeaderTimeout`.
- `server/internal/api/sessions.go:219-224` decodes directly from `r.Body` with
  no `http.MaxBytesReader`, EOF/trailing-value check, or shared content-type
  policy; actions and most project/session mutations reuse this helper.
- A client that completes headers and then sends a body indefinitely can hold a
  handler and connection. A large JSON body is also accepted until decoding or
  allocation fails.
- `readInitialAttachInput` can retain multiple binary frames received during its
  two-second resize window; each frame is capped, but their aggregate is not.

Recommendation:

- Replace `decodeJSON` with one shared strict-enough decoder that applies a small
  endpoint-appropriate `http.MaxBytesReader` limit, rejects trailing JSON, and
  returns stable errors.
- Add a request-body read deadline or per-request middleware for ordinary API
  routes without breaking long-lived WebSocket attaches.
- Set an explicit idle timeout and document why global write/read timeouts are
  or are not safe for the WebSocket route.
- Cap the aggregate pre-resize binary buffer, or accept only the most recent
  resize plus a bounded amount of input.

The Go standard library documents custom server timeouts and
`MaxBytesReader` as the primitives for these controls
([`net/http` server documentation](https://pkg.go.dev/net/http#Server),
[`MaxBytesReader`](https://pkg.go.dev/net/http#MaxBytesReader)).

### P1-04 — Native bearer tokens are persisted in plain app preferences

Evidence:

- `ConnectionRecord.token` is encoded with the full connection array under one
  UserDefaults key (`macos/atc/Settings/ConnectionsModel.swift`).
- The earlier settings plan explicitly deferred Keychain work. That was a
  reasonable vertical-slice decision, but the app now has durable multi-server
  settings, distribution automation, and remote access, so the deferral has
  reached its useful limit.
- The web admin UI separately persists its token in `localStorage`; that needs a
  browser threat-model decision and should not dictate the native storage
  choice.

Recommendation:

- Keep connection identity, name, URL, and ordering in UserDefaults.
- Store each token as a Keychain item keyed by stable connection UUID; preferably
  use the data-protection keychain on supported macOS versions.
- Make add/update/delete atomic from the user's perspective and migrate existing
  plaintext tokens once, deleting the old value only after a verified Keychain
  write.
- Inject a credential-store protocol so tests can use an in-memory
  implementation without prompting Keychain UI.
- Decide separately whether the browser admin UI should use session-only token
  storage, localStorage plus a strict CSP, or a server-issued cookie.

Apple recommends Keychain Services for network credentials because it provides
encrypted secret storage without custom cryptography
([Apple Keychain guidance](https://developer.apple.com/documentation/security/adding-a-password-to-the-keychain)).

### P1-05 — The API contract is manually duplicated across three implementations

Evidence:

- Go owns wire structs and routes in `server/internal/api`.
- TypeScript repeats those models and endpoint paths in
  `server/web/src/lib/api.ts`.
- Swift repeats them again in `packages/ATCKit`.
- The web API-reference data is a fourth manually maintained description. It
  already describes session start as returning `starting`, while the current
  synchronous service marks and returns the session `running` after zmx launch.
- The monorepo brief deferred contract tooling until multiple clients existed.
  There are now two independent clients plus the CLI.

Recommendation:

- Do not jump immediately to heavy code generation.
- First add canonical response/request fixtures under a shared
  `packages/contracts` or `server/docs/specs/fixtures` location.
- Have Go handler tests produce or validate the fixtures, Swift decode them, and
  TypeScript validate them with a small runtime schema layer.
- Add one route inventory used by docs/tests so endpoint documentation cannot
  silently omit or misstate current behavior.
- Re-evaluate OpenAPI or generated models only after the fixture approach shows
  its limits.

This keeps the current simple architecture while removing the highest-risk form
of duplication: independently maintained public contracts.

### P2-01 — Terminal registry presence is treated as a live connection

Evidence:

- `AppModel.terminals` intentionally retains controllers and terminal surfaces
  to preserve scrollback while navigating.
- `hasLiveTerminals`, sidebar `connectedRefs`, and the header's `isConnected`
  check only whether a controller exists.
- When an attach reaches `.ended(.sessionEnded)`, `.serverError`, or
  `.transportFailure`, the controller stays in the dictionary.
- The UI can therefore show a terminal as “Connected,” offer “Disconnect,” and
  warn that a connection edit will disconnect live terminals when no WebSocket
  is live.

Recommendation:

- Preserve controllers for terminal history, but expose a derived
  `isActivelyAttached`/phase property and use it for connection indicators and
  destructive-save confirmation.
- Reserve dictionary membership for ownership/lifetime, not connection state.
- Add tests covering every ended phase and the sidebar/header/settings
  affordances.

### P2-02 — Refresh ownership is duplicated and overlapping refresh state is inaccurate

Evidence:

- `ConnectionRuntime` owns and starts the real seven-second polling task.
- `ProjectsStore.pollLoop()` and `SessionsStore.pollLoop()` are unused legacy
  polling implementations with comments that no longer describe actual
  ownership.
- Projects, Sessions, and Actions stores all use a generation token to prevent
  stale data from winning, which is good, but every invocation unconditionally
  clears `isLoading` in `defer`. An older request can finish while a newer request
  is still active and incorrectly set `isLoading = false` and
  `hasLoadedOnce = true`.
- Mutation methods merge the server response and then start an unstructured
  refresh task. Filter changes and manual refresh can overlap with those tasks.
- `AppModel.refreshAll()` refreshes independent connections sequentially, so one
  unreachable connection delays every later connection.

Recommendation:

- Keep polling ownership only in `ConnectionRuntime`; delete the unused store
  loops and stale comments.
- Represent refresh state with a current-task identity or in-flight count, and
  only let the current generation settle visible loading/error state.
- Either cancel superseded refreshes or keep the generation design but make its
  lifecycle complete.
- Refresh runtimes concurrently with a task group.
- Prefer structured follow-up refreshes where practical; where fire-and-forget
  is intentional, store/cancel the task explicitly.

### P2-03 — Frontend validation is below the standard of the Go and Swift surfaces

Evidence:

- `package.json` has only `dev`, `build`, and `preview` scripts.
- There is no `check`, `typecheck`, `lint`, or `test` command.
- `tsc --noEmit` cannot run because `@types/node` is missing.
- The production build emits `state_referenced_locally` warnings at
  `project-editor.svelte:25-26`.
- API responses are trusted with TypeScript assertions; malformed or drifted
  responses are not validated at runtime.
- The web UI has meaningful stateful workflows—action editing, project
  lifecycle, session start, auth, and terminal attach—but no automated tests.

Recommendation:

- Add `@types/node` and `svelte-check`; expose one `check` command that is clean
  locally and in CI.
- Fix the project-editor initialization warning by making the component's draft
  reset contract explicit (keyed remount or an intentional effect), not by
  suppressing the warning.
- Add focused tests for the shared API client and the highest-risk state
  transitions; a large browser suite is unnecessary at this stage.
- Introduce runtime response validation as part of P1-05 rather than adding a
  separate ad hoc model layer.

### P2-04 — The Go suite is not portable to the repository's primary development OS

Evidence:

- `TestListSorting` expects both `AB` and `ab` to exist in one temporary
  directory.
- Default macOS APFS volumes are case-insensitive, so one creation aliases the
  other and the test fails.
- CI is Linux-only, so this defect is invisible remotely even though macOS is a
  first-class product and development environment.

Recommendation:

- Separate deterministic comparator unit tests from filesystem integration
  tests. Test case-only ties by constructing `Entry` values directly.
- Keep the directory integration test limited to names portable across
  supported filesystems.
- Run the full Go suite on macOS at least until platform-specific daemon/socket
  behavior is stable.

### P2-05 — Active documentation still defines the retired atc-era system

Evidence:

- The monorepo brief says to retire atc from product language,
  documentation, new code, and install paths.
- Numerous non-archived root and server documents still refer to `ATCAPI`,
  `ATCClient`, `ATCKit`, `HTTPATCClient`, `ATC_*`, the separate
  atc repository, and obsolete paths such as `Packages/ATCKit`.
- Several server ADR filenames and active plan/spec files still use atc as
  the owning product name.
- These documents are discoverable beside current plans without a superseded or
  historical marker, so a new contributor can follow technically obsolete
  instructions.

Recommendation:

- Classify docs as current, historical, or superseded.
- Update current product/architecture documents end to end to atc names
  and current monorepo paths.
- Move useful historical implementation plans under an archive directory or add
  a prominent superseded header linking to the current source of truth.
- Preserve ADR decision history, but annotate renamed concepts and add successor
  ADRs when the present decision differs.
- Add a lightweight docs check for retired executable identifiers such as
  `ATC_`, `ATCKit`, and old package paths outside explicitly historical
  locations.

### P2-06 — macOS tests pass while the hosted UI emits runtime warnings

Evidence:

- Optional Pickers are hosted with a nil selection and no matching nil tag in
  empty/loading states, producing “selection nil is invalid” warnings.
- The test host also reports a reentrant `NSTableView` delegate operation and
  explicitly warns that the behavior will become an assertion in the future.
- These warnings are easy to miss because the test command exits successfully.

Recommendation:

- Add an explicit nil/placeholder choice for optional Pickers or avoid hosting
  the Picker until a valid selection exists.
- Isolate the reentrant table warning to a specific hosting test and state
  mutation, then remove the reentrant update rather than suppressing the log.
- Keep UI-hosting smoke tests; they are already catching problems unit tests
  cannot see.

### P3-01 — Decide and document the macOS compatibility/security floor

Evidence:

- The Xcode project deployment target is macOS 26.5 in project and target
  configurations, while `ATCKit` declares macOS 15.
- The app disables App Sandbox and allows arbitrary network loads through ATS.
- Those choices may be appropriate for a terminal client that reads Ghostty
  config and connects to HTTP servers over a tailnet, but they are currently
  spread across an old POC plan, build settings, and an Info.plist comment rather
  than one current policy.
- The connection editor accepts arbitrary cleartext HTTP origins and can attach a
  bearer token; users receive no warning when the URL is neither loopback nor
  HTTPS.

Recommendation:

- Choose a deliberate minimum supported macOS version and keep the project,
  package, CI runner, and distribution docs aligned.
- Record the reasons for disabling App Sandbox and the conditions that would
  allow re-enabling it.
- Make cleartext-HTTP policy explicit in connection validation/UI. A safe default
  is HTTPS for non-loopback connections, with a deliberate override for trusted
  encrypted overlays or SSH tunnels.
- Narrow ATS exceptions if the chosen connection policy makes that possible.

### P3-02 — Small readability and performance cleanup

Evidence and recommendations:

- Run gofmt and enforce it; the two current formatting diffs are small but prove
  the gate is absent.
- `AttachConnection` uses an unbounded `AsyncStream` for outbound terminal data.
  Keystrokes are normally low volume, but a stalled socket plus paste can grow
  memory. Use a bounded/coalescing strategy that preserves input order and keeps
  the most recent resize.
- `TerminalSessionController` drains `pendingOutput` with repeated
  `removeFirst()`, which is quadratic for a large replay. Drain by index or swap
  the array once the surface is ready.
- `AppModel` force-unwraps a URL based on a persistence invariant. The invariant
  is currently tested, but a failable factory with an explicit corrupted-record
  path would make migration failures easier to diagnose.
- The 638.88 kB web chunk is the Ghostty terminal route and is already route
  isolated, so it is not currently a general startup-performance problem.
  Record a bundle budget and monitor it rather than introducing premature
  splitting.
- The 636-line preview mock and 565-line Actions settings view are still
  understandable, but they are the first candidates for extraction if another
  feature increases them materially. Split by coherent domain behavior, not by
  arbitrary line count.

Recommendation:

Stabilize in this order:

1. **Make the truth visible:** add CI/check commands, fix the macOS Go test,
   restore gofmt cleanliness, fix Svelte/Picker warnings, and get all current
   checks green.
2. **Close trust-boundary gaps:** fail closed for unauthenticated remote binds,
   bound HTTP bodies/lifetimes, and move native secrets to Keychain.
3. **Protect the monorepo contract:** add shared API fixtures and cross-client
   contract tests before adding more endpoints.
4. **Fix lifecycle semantics:** separate retained terminal history from active
   connection state and consolidate refresh/poll ownership.
5. **Make documentation current:** archive or update atc-era plans and record
   the macOS compatibility/network policy.
6. **Then resume feature work.** Preserve the existing service boundaries and
   tests; they are the strongest part of the foundation.

Open Questions:

- **Minimum macOS version:** recommended default is the oldest version actually
  supported by the chosen Ghostty package and used APIs, not the developer
  machine's current patch release. Confirm the desired audience before choosing
  the number.
- **Remote trust model:** recommended default is token-required for every
  non-loopback bind. If tailnet identity alone is an intentional supported mode,
  make it an explicit insecure/trusted-network override with prominent docs.
- **API contract mechanism:** recommended first step is shared fixtures plus
  runtime validation, not full code generation. Revisit OpenAPI after the next
  meaningful contract expansion.
- **Browser token persistence:** native Keychain migration should proceed
  independently. Decide whether the admin UI is a trusted-local tool or a
  remotely exposed product surface before choosing localStorage, session
  storage, or cookie-based auth.
