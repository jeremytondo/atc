import ATCAppServerAPI
import ATCAppServerTransport
import AppKit
import Foundation

@testable import ATC

// Shared harness for the App Server-era suites: an isolated AppModel wired to
// a `ScriptableAppServerClient`, a scripted SSE stream, and terminal
// controllers whose attach WebSocket is a test-driven AsyncStream. Nothing
// here touches the real keychain, the user's defaults, the network, or zmx.

// MARK: - Run loop pumping

/// Pumps the main run loop for a fixed duration so hosted SwiftUI/AppKit
/// hierarchies can process layout, timers, and focus changes.
@MainActor
func pump(seconds: TimeInterval) {
    RunLoop.main.run(until: Date(timeIntervalSinceNow: seconds))
}

/// Pumps the main run loop in short slices until `condition` holds or
/// `timeout` elapses. For hosted-view work only: it starves main-actor Tasks,
/// so anything driven by `async` logic must use `settle(until:)` instead.
@MainActor
func pump(until condition: () -> Bool, timeout: TimeInterval = 2) {
    let deadline = Date(timeIntervalSinceNow: timeout)
    while !condition(), Date() < deadline {
        RunLoop.main.run(until: Date(timeIntervalSinceNow: 0.02))
    }
}

// MARK: - Async settling

/// Suspends until `condition` holds, giving pending main-actor Tasks — and
/// timer-driven steps such as the runtime's ~100 ms coalescing window — room
/// to run. `timeout` is a failure bound, never the thing under test: every
/// caller waits on a real observable condition, and a passing test returns as
/// soon as it holds.
///
/// Use this, not `pump(until:)`, for anything Task-driven: run-loop pumping
/// starves main-actor Tasks.
@MainActor
func settle(until condition: () -> Bool, timeout: Duration = .seconds(5)) async {
    let deadline = ContinuousClock.now + timeout
    while !condition(), ContinuousClock.now < deadline {
        await Task.yield()
        try? await Task.sleep(for: .milliseconds(1))
    }
}

/// Drains already-queued main-actor Tasks without waiting for any outcome.
/// Only for negative assertions whose ordering is already pinned by a
/// preceding `settle(until:)`.
@MainActor
func drainPendingTasks(iterations: Int = 20) async {
    for _ in 0..<iterations {
        await Task.yield()
    }
}

// MARK: - Scripted event stream

/// The SSE seam under test control: hand `stream` to a runtime through
/// `eventStreamFactory` and push `.connected` / `.change` / `.disconnected`
/// from the test. One consumer only — a stream drives exactly one runtime.
nonisolated final class ScriptedEventStream: @unchecked Sendable {
    let stream: AsyncStream<ResourceEventStream.Event>
    private let continuation: AsyncStream<ResourceEventStream.Event>.Continuation

    init() {
        (stream, continuation) = AsyncStream.makeStream(of: ResourceEventStream.Event.self)
    }

    func connect() {
        continuation.yield(.connected)
    }

    func change(
        _ resource: Components.Schemas.ResourceChangedEvent.ResourcePayload,
        id: String = "id",
        change: Components.Schemas.ResourceChangedEvent.ChangePayload = .updated
    ) {
        continuation.yield(.change(.init(resource: resource, id: id, change: change)))
    }

    func disconnect() {
        continuation.yield(.disconnected)
    }

    func finish() {
        continuation.finish()
    }

    /// A stream that never yields: the default for suites that drive stores
    /// directly and must not have a live SSE loop refreshing underneath them.
    static func inert() -> AsyncStream<ResourceEventStream.Event> {
        AsyncStream { _ in }
    }
}

// MARK: - Scripted thread event stream

/// The per-thread SSE seam under test control: hand `factory` to a runtime
/// through `threadEventStreamFactory`; every Chat model's subscription is
/// recorded (thread id and its live `after` cursor) and driven from the
/// test with `connect` / `send` / `disconnect`.
nonisolated final class ScriptedThreadEventStream: @unchecked Sendable {
    struct Subscription {
        let threadID: String
        /// The model's resume cursor, read live — what a reconnect would ask.
        let after: @Sendable () -> Int?
        let continuation: AsyncStream<ThreadEventStream.Event>.Continuation
    }

    private let lock = NSLock()
    private var recorded: [Subscription] = []

    var subscriptions: [Subscription] { lock.withLock { recorded } }

    var factory: ConnectionRuntime.ThreadEventStreamFactory {
        { [self] _, threadID, _, after in
            let (stream, continuation) = AsyncStream.makeStream(of: ThreadEventStream.Event.self)
            lock.withLock {
                recorded.append(Subscription(threadID: threadID, after: after, continuation: continuation))
            }
            return stream
        }
    }

    /// The most recent subscription for a thread (a model subscribes once
    /// and resumes on the same stream).
    private func subscription(_ threadID: String) -> Subscription? {
        subscriptions.last { $0.threadID == threadID }
    }

    /// Connects as the live stream would: it asks the resume cursor first
    /// (that is what a real (re)connect sends as `after`), then opens.
    func connect(_ threadID: String) {
        guard let subscription = subscription(threadID) else { return }
        _ = subscription.after()
        subscription.continuation.yield(.connected)
    }

    func send(_ threadID: String, _ event: ThreadEvent) {
        subscription(threadID)?.continuation.yield(.event(event))
    }

    func disconnect(_ threadID: String) {
        subscription(threadID)?.continuation.yield(.disconnected)
    }
}

// MARK: - Attach harness

/// Stands in for the attach WebSocket. Every connection attempt is recorded
/// and left open so tests can prove that a late event from a replaced attach
/// is generation-gated. Nonisolated so it can be a default argument.
nonisolated final class AttachHarness {
    struct Attempt {
        let url: URL
        let continuation: AsyncStream<AttachEvent>.Continuation
    }

    private(set) var attempts: [Attempt] = []

    var attemptCount: Int { attempts.count }

    func makeHandle(url: URL, headers _: [String: String]) -> TerminalAttachHandle {
        let (stream, continuation) = AsyncStream.makeStream(of: AttachEvent.self)
        attempts.append(Attempt(url: url, continuation: continuation))
        return TerminalAttachHandle(
            start: { stream },
            enqueue: { _ in },
            enqueueResize: { _, _ in },
            close: {}
        )
    }

    func send(_ event: AttachEvent, attempt: Int, finish: Bool = false) {
        attempts[attempt].continuation.yield(event)
        if finish {
            attempts[attempt].continuation.finish()
        }
    }
}

/// Records the backoff delays a controller asked to sleep, and returns
/// immediately so retries are deterministic and instantaneous.
nonisolated final class RetryDelayRecorder {
    private(set) var delays: [Duration] = []

    func sleep(for duration: Duration) async throws {
        delays.append(duration)
    }
}

/// Advanceable stand-in for `ContinuousClock.now`, so stability rules are
/// measured against test-controlled time instead of slept real time.
nonisolated final class FakeClock {
    private(set) var now = ContinuousClock.now

    func advance(by duration: Duration) {
        now += duration
    }
}

/// Scripted answer for a controller's reconciled `checkLive` read: `true`
/// (still live), `false` (gone), or `nil` (server unreachable).
nonisolated final class CheckLiveStub {
    var result: Bool?
    private(set) var callCount = 0

    init(result: Bool? = nil) {
        self.result = result
    }

    func check() async -> Bool? {
        callCount += 1
        return result
    }
}

// MARK: - Isolated AppModel

/// One Connection's worth of test-owned machinery, kept together so a test
/// can seed the server, push events, and inspect attaches from one value.
@MainActor
struct TestModel {
    let model: AppModel
    let client: ScriptableAppServerClient
    let events: ScriptedEventStream?
    let attach: AttachHarness
    let record: ConnectionRecord

    var runtime: ConnectionRuntime {
        // Force-unwrapped deliberately: every helper here adds the Connection
        // before returning, so a nil runtime is a harness bug, not a test case.
        model.runtime(id: record.id)!
    }

    var connectionID: UUID { record.id }

    func threadRef(_ id: String) -> ThreadRef {
        ThreadRef(connectionID: record.id, threadID: id)
    }

    func terminalRef(_ id: String) -> TerminalRef {
        TerminalRef(connectionID: record.id, terminalID: id)
    }

    func projectRef(_ id: String) -> ProjectRef {
        ProjectRef(connectionID: record.id, projectID: id)
    }
}

/// An AppModel with one Connection, an ephemeral defaults suite and in-memory
/// credential store, a scripted client, a scripted (or inert) event stream,
/// and terminal controllers that never open a socket.
@MainActor
func makeModel(
    client: ScriptableAppServerClient = ScriptableAppServerClient(),
    events: ScriptedEventStream? = nil,
    threadEvents: ScriptedThreadEventStream? = nil,
    attach: AttachHarness = AttachHarness(),
    attachmentBudget: Int = 12,
    threadNotifier: ThreadNotifier? = nil,
    name: String = "A",
    urlString: String = "http://a:1"
) async throws -> TestModel {
    let store = makeConnectionsStore()
    let record = try store.add(name: name, urlString: urlString, token: "")
    let model = makeAppModel(
        connections: store,
        client: client,
        events: events,
        threadEvents: threadEvents,
        attach: attach,
        threadNotifier: threadNotifier,
        attachmentBudget: attachmentBudget
    )
    // A started runtime probes immediately (first-contact reachability).
    // Settle that probe here so tests never race it: whatever the client
    // held at construction is loaded, and every later mutation is the
    // test's own doing.
    let runtime = model.runtime(id: record.id)!
    await settle(until: {
        runtime.projects.hasLoadedOnce && runtime.threads.hasLoadedOnce
            && runtime.terminals.hasLoadedOnce && runtime.agents.hasLoadedOnce
    })
    return TestModel(
        model: model,
        client: client,
        events: events,
        attach: attach,
        record: record
    )
}

/// The multi-Connection variant: no Connection is added up front, and every
/// runtime shares one client. Connection-lifecycle tests add their own.
@MainActor
func makeEmptyAppModel(
    client: ScriptableAppServerClient = ScriptableAppServerClient(),
    attach: AttachHarness = AttachHarness()
) -> AppModel {
    makeAppModel(
        connections: makeConnectionsStore(),
        client: client,
        events: nil,
        threadEvents: nil,
        attach: attach,
        threadNotifier: nil,
        attachmentBudget: 12
    )
}

@MainActor
func makeConnectionsStore() -> ConnectionsStore {
    let suite = "ATCTests.\(UUID().uuidString)"
    let defaults = UserDefaults(suiteName: suite)!
    defaults.removePersistentDomain(forName: suite)
    return ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
}

@MainActor
private func makeAppModel(
    connections: ConnectionsStore,
    client: ScriptableAppServerClient,
    events: ScriptedEventStream?,
    threadEvents: ScriptedThreadEventStream?,
    attach: AttachHarness,
    threadNotifier: ThreadNotifier?,
    attachmentBudget: Int
) -> AppModel {
    AppModel(
        connections: connections,
        clientFactory: { _, _ in client },
        terminalControllerFactory: { terminalID, runtime in
            makeTestController(
                terminalID: terminalID,
                runtime: runtime,
                attach: attach
            )
        },
        terminalRecoveryMonitor: .disabled(),
        // An absent factory would leave the runtime on the live SSE stream,
        // pointing the suite at a real socket.
        eventStreamFactory: { _, _ in events?.stream ?? ScriptedEventStream.inert() },
        // Same rule for the per-thread streams: never a live socket.
        threadEventStreamFactory: threadEvents?.factory ?? { _, _, _, _ in AsyncStream { _ in } },
        threadNotifier: threadNotifier,
        attachmentBudget: attachmentBudget
    )
}

/// The production controller wiring with only the socket and the clock
/// replaced, so attach lifecycle stays under test.
@MainActor
func makeTestController(
    terminalID: String,
    runtime: ConnectionRuntime,
    attach: AttachHarness,
    // `.default` is main-actor-isolated, so it is resolved in the body rather
    // than as a default argument.
    retryPolicy: TerminalSessionController.RetryPolicy? = nil
) -> TerminalSessionController {
    TerminalSessionController(
        terminalID: terminalID,
        endpoint: AttachEndpoint(baseURL: runtime.baseURL, headers: runtime.transportHeaders),
        checkLive: { [weak runtime] in
            await runtime?.terminals.checkLive(id: terminalID) ?? nil
        },
        connectionFactory: attach.makeHandle,
        retryPolicy: retryPolicy ?? .default,
        retrySleep: { _ in },
        jitterUnit: { 0.5 }
    )
}
