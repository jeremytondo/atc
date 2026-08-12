import SwiftUI
import Testing
@testable import ATC

/// The ATC-160 display translation: `idle + unread` reads "Done" in the
/// accent color; every other combination defers to the activity labels. A
/// display-only rule — the server vocabulary has no completed state.
@Suite("Thread display translation")
struct ThreadDisplayTests {
    @Test("an idle unread thread reads Done in accent")
    func doneOverlay() {
        let done = Fixtures.thread(id: "t", activityState: .idle, unread: true)
        #expect(done.statusLabel == "Done")
        #expect(done.detailLabel == "Done")
        #expect(done.statusColor == .accentColor)
    }

    @Test("a viewed idle thread fades back to Idle")
    func viewedFadesBack() {
        let idle = Fixtures.thread(id: "t", activityState: .idle, unread: false)
        #expect(idle.statusLabel == "Idle")
        #expect(idle.detailLabel == "Idle")
        #expect(idle.statusColor == Color.secondary)
    }

    @Test("the overlay never rewrites a busy or unknown state")
    func busyStatesUntouched() {
        // Unread can be true mid-turn (an unviewed finish followed by a new
        // prompt); the running state wins until the thread is idle again.
        let working = Fixtures.thread(id: "t", activityState: .working, unread: true)
        #expect(working.statusLabel == "Running")
        #expect(working.statusColor == .green)

        let needsInput = Fixtures.thread(id: "t", activityState: .needsInput, unread: true)
        #expect(needsInput.statusLabel == "Needs you")
        #expect(needsInput.statusColor == .orange)

        let unknown = Fixtures.thread(id: "t", activityState: .unknown, unread: true)
        #expect(unknown.statusLabel == nil)
    }
}
