import ATCAppServerAPI
import Foundation
import Testing

@testable import ATC

/// ThreadNotifier: the trigger, withdraw, and baseline decisions. Every case
/// here is about *state*, not about a remembered event — a banner claims what
/// the thread is right now, and a claim that stops being true comes down.
@MainActor
@Suite("ThreadNotifier")
struct ThreadNotifierTests {
    /// What the delivery seam was asked to show. Nothing in this suite reaches
    /// `UNUserNotificationCenter`.
    final class Recorder {
        struct Banner: Equatable {
            let id: String
            let title: String
            let body: String
        }

        private(set) var posts: [Banner] = []
        private(set) var withdrawn: [String] = []

        /// A notifier that is switched on and backgrounded — the state in
        /// which banners are actually meant to fire.
        func makeNotifier() -> ThreadNotifier {
            let notifier = ThreadNotifier()
            notifier.isEnabled = { true }
            notifier.isAppActive = { false }
            notifier.deliver = { id, title, body in
                self.posts.append(Banner(id: id, title: title, body: body))
            }
            notifier.withdraw = { self.withdrawn.append($0) }
            return notifier
        }
    }

    private let connectionID = UUID()

    private func input(_ threads: [ATCThread], isResolved: Bool = true) -> ThreadNotifier.ConnectionInput {
        ThreadNotifier.ConnectionInput(
            connectionID: connectionID,
            isResolved: isResolved,
            projects: [Fixtures.project(id: "prj", name: "App")],
            threads: threads
        )
    }

    private func ref(_ id: String) -> ThreadRef {
        ThreadRef(connectionID: connectionID, threadID: id)
    }

    @Test("a connection's first load seeds the baseline and notifies nothing")
    func firstLoadIsSilent() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()

        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", unread: true),
                Fixtures.thread(id: "thr2", activityState: .needsInput),
            ])
        ])

        #expect(recorder.posts.isEmpty)
        #expect(recorder.withdrawn.isEmpty)
    }

    @Test("a finish after the baseline notifies, titled by the thread and bodied by its project")
    func finishNotifies() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", name: "Fix login")])])

        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", name: "Fix login", unread: true)
            ])
        ])

        #expect(
            recorder.posts == [
                Recorder.Banner(
                    id: ref("thr1").notificationIdentifier,
                    title: "Fix login",
                    body: "Finished · App"
                )
            ])
    }

    @Test("needs_input notifies, and outranks the unread a finished turn also sets")
    func needsInputOutranksFinished() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", name: "Fix login")])])

        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", name: "Fix login", activityState: .needsInput, unread: true)
            ])
        ])
        #expect(recorder.posts.map(\.body) == ["Needs your input · App"])

        // Still needing input is the same claim: no second banner for it.
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", name: "Fix login", activityState: .needsInput, unread: true)
            ])
        ])
        #expect(recorder.posts.count == 1)
    }

    @Test("nothing notifies while atc is frontmost, and nothing is replayed once it isn't")
    func frontmostIsSilentAndSpent() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.isAppActive = { true }
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])

        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", unread: true)])])
        #expect(recorder.posts.isEmpty)

        // Switching away later must not deliver a backlog about a finish that
        // already happened: the sidebar's "Done" is the durable record.
        notifier.isAppActive = { false }
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", unread: true)])])
        #expect(recorder.posts.isEmpty)
    }

    @Test("answering a prompt takes the banner down instead of re-alerting as finished")
    func leavingNeedsInputNeverRealerts() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", activityState: .needsInput, unread: true)
            ])
        ])

        // Answering in the TUI resumes the turn. Unread was already set, so
        // nothing finished that the user has not been told about.
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", activityState: .working, unread: true)
            ])
        ])

        #expect(recorder.posts.count == 1)
        #expect(recorder.withdrawn == [ref("thr1").notificationIdentifier])
    }

    @Test("clearing unread withdraws the banner — viewing the thread anywhere takes it down")
    func clearedUnreadWithdraws() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", unread: true)])])

        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])

        #expect(recorder.withdrawn == [ref("thr1").notificationIdentifier])
    }

    @Test("a thread that leaves the active list withdraws its banner")
    func archivedThreadWithdraws() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", unread: true)])])

        notifier.reconcile(connections: [input([])])

        #expect(recorder.withdrawn == [ref("thr1").notificationIdentifier])
    }

    @Test("an unresolved connection is skipped — connection loss is not a state change")
    func unresolvedConnectionIsSkipped() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", unread: true)])])

        notifier.reconcile(connections: [input([], isResolved: false)])

        #expect(recorder.withdrawn.isEmpty)
        #expect(recorder.posts.count == 1)
    }

    @Test("the preference gates posting, not tracking")
    func disabledPostsNothing() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.isEnabled = { false }
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])

        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", unread: true)])])
        #expect(recorder.posts.isEmpty)

        // Nothing was delivered, so nothing is withdrawn when it clears.
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])
        #expect(recorder.withdrawn.isEmpty)
    }

    @Test("forgetting a connection withdraws its banners and re-seeds silently")
    func forgottenConnectionReseeds() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", unread: true)])])

        notifier.forget(connectionID: connectionID)
        #expect(recorder.withdrawn == [ref("thr1").notificationIdentifier])

        // The rebuilt connection's first load is a first load.
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1", unread: true)])])
        #expect(recorder.posts.count == 1)
    }

    @Test("a suppressed trigger still withdraws a banner whose claim went stale")
    func suppressedTriggerStillWithdraws() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", activityState: .needsInput)
            ])
        ])
        #expect(recorder.posts.count == 1)

        // The turn finishes without input while ATC is frontmost: the
        // .finished trigger is suppressed, but the needs-input claim is now
        // false and the banner must still come down.
        notifier.isAppActive = { true }
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", activityState: .idle, unread: true)
            ])
        ])

        #expect(recorder.posts.count == 1)
        #expect(recorder.withdrawn == [ref("thr1").notificationIdentifier])
    }

    @Test("an unknown blip neither withdraws nor re-alerts a needs-input banner")
    func unknownBlipIsTransparent() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", activityState: .needsInput)
            ])
        ])
        #expect(recorder.posts.count == 1)

        // The server loses sight of the provider (socket blip, restart) and
        // then finds it again: absence of evidence, not a state change.
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", activityState: .unknown)
            ])
        ])
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", activityState: .needsInput)
            ])
        ])

        #expect(recorder.posts.count == 1)
        #expect(recorder.withdrawn.isEmpty)
    }

    @Test("unread landing a pass behind needs_input never demotes the banner to finished")
    func lateUnreadKeepsNeedsInput() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", activityState: .needsInput)
            ])
        ])

        // activityState (live provider evidence) and unread (turn
        // persistence) can land in separate refreshes.
        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1", activityState: .needsInput, unread: true)
            ])
        ])

        #expect(recorder.posts.map(\.body) == ["Needs your input · App"])
        #expect(recorder.withdrawn.isEmpty)
    }

    @Test("a thread that appears after the baseline seeds silently")
    func newThreadSeedsSilently() {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        notifier.reconcile(connections: [input([Fixtures.thread(id: "thr1")])])

        notifier.reconcile(connections: [
            input([
                Fixtures.thread(id: "thr1"),
                Fixtures.thread(id: "thr2", unread: true),
            ])
        ])

        #expect(recorder.posts.isEmpty)
    }

    @Test("a notification identifier round-trips to its thread")
    func identifierRoundTrip() {
        let original = ref("019012ab-0000-7000-8000-00000000abcd")
        #expect(ThreadRef(notificationIdentifier: original.notificationIdentifier) == original)
        #expect(ThreadRef(notificationIdentifier: "thread/not-a-uuid/thr1") == nil)
    }

    @Test("a finish arriving through the stores notifies once, however many windows are open")
    func modelWiringNotifiesOnce() async throws {
        let recorder = Recorder()
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client, threadNotifier: recorder.makeNotifier())
        // Held for the test's duration: the model's window registry is weak.
        let windows = [WindowState(), WindowState()]
        for window in windows { test.model.registerWindow(window) }
        // The first load must have seeded before the finish lands, or the
        // finish would be seeded silently instead of notifying.
        await drainPendingTasks()

        var threads = client.threads
        threads[threads.firstIndex { $0.id == "thr3" }!].unread = true
        client.threads = threads
        await test.runtime.threads.refresh()
        await settle(until: { !recorder.posts.isEmpty })

        #expect(recorder.posts.map(\.id) == [test.threadRef("thr3").notificationIdentifier])
        #expect(recorder.posts.map(\.body) == ["Finished · App"])
        #expect(windows.count == 2)
    }

    @Test("a click that launched the app opens its thread once a window exists")
    func clickBeforeAnyWindowIsHonored() async throws {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client, threadNotifier: notifier)

        notifier.onOpenThread?(test.threadRef("thr3"))

        let window = WindowState()
        test.model.registerWindow(window)
        await settle(until: { window.selectedThread == test.threadRef("thr3") })
        #expect(window.selectedThread == test.threadRef("thr3"))
    }

    @Test("a needs_input transition arriving through the stores notifies")
    func modelWiringNotifiesNeedsInput() async throws {
        let recorder = Recorder()
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client, threadNotifier: recorder.makeNotifier())
        // The first load must have seeded before the transition lands.
        await drainPendingTasks()

        var threads = client.threads
        threads[threads.firstIndex { $0.id == "thr3" }!].activityState = .needsInput
        client.threads = threads
        await test.runtime.threads.refresh()
        await settle(until: { !recorder.posts.isEmpty })

        #expect(recorder.posts.map(\.id) == [test.threadRef("thr3").notificationIdentifier])
        #expect(recorder.posts.map(\.body) == ["Needs your input · App"])
    }

    @Test("a banner click opens in the most recently keyed window, not the last registered")
    func clickOpensInKeyWindow() async throws {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client, threadNotifier: notifier)
        let first = WindowState()
        let second = WindowState()
        test.model.registerWindow(first)
        test.model.registerWindow(second)
        await drainPendingTasks()

        // The user works in the first window; the second merely opened later.
        test.model.noteWindowKeyed(first)
        notifier.onOpenThread?(test.threadRef("thr3"))

        await settle(until: { first.selectedThread == test.threadRef("thr3") })
        #expect(first.selectedThread == test.threadRef("thr3"))
        #expect(second.selectedThread == nil)
    }

    @Test("the user's own navigation supersedes a parked banner click")
    func userNavigationSupersedesParkedClick() async throws {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client, threadNotifier: notifier)
        // Parked: no window is registered yet.
        notifier.onOpenThread?(test.threadRef("thr3"))

        _ = try await test.model.openThread(test.threadRef("thr1"))

        // A window arriving later must not replay the superseded click.
        let window = WindowState()
        test.model.registerWindow(window)
        await drainPendingTasks()
        #expect(window.selectedThread == nil)
    }

    @Test("a connection rebuild drops a parked banner click")
    func rebuildDropsParkedClick() async throws {
        let recorder = Recorder()
        let notifier = recorder.makeNotifier()
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client, threadNotifier: notifier)
        // Parked: no window is registered yet.
        notifier.onOpenThread?(test.threadRef("thr3"))

        // A URL change tears the runtime down and rebuilds it under the same
        // Connection id; the stale click must not replay against the rebuild.
        try test.model.updateConnection(
            id: test.connectionID,
            name: test.record.name,
            urlString: "http://b:2",
            token: test.record.token
        )
        let window = WindowState()
        test.model.registerWindow(window)
        await settle(until: { test.model.runtime(id: test.connectionID)?.threads.isResolved == true })
        await drainPendingTasks()
        #expect(window.selectedThread == nil)
    }
}
