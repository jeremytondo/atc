import Foundation
import Testing
import ATCAPI
@testable import ATC

@MainActor
@Suite("Detail canvas")
struct DetailCanvasTests {
    private func session(status: SessionStatus) -> Session {
        Session(
            id: "session",
            environment: "host",
            workingDir: "/home/dev",
            status: status,
            createdAt: .now,
            updatedAt: .now
        )
    }

    @Test("terminal visibility matches every cover state")
    func showsTerminal() {
        let live = session(status: .live)
        let ended = session(status: .ended)

        #expect(!DetailCanvas.showsTerminal(
            isDashboard: true,
            session: live,
            hasController: true
        ))
        #expect(!DetailCanvas.showsTerminal(
            isDashboard: false,
            session: nil,
            hasController: true
        ))
        #expect(!DetailCanvas.showsTerminal(
            isDashboard: false,
            session: ended,
            hasController: true
        ))
        #expect(!DetailCanvas.showsTerminal(
            isDashboard: false,
            session: live,
            hasController: false
        ))
        #expect(DetailCanvas.showsTerminal(
            isDashboard: false,
            session: live,
            hasController: true
        ))
    }

    @Test("covers use the app canvas and terminals use preference resolution")
    func backingColor() {
        let preferences = TerminalPreferences(background: "ff0000")

        #expect(DetailCanvas.backingColor(
            showsTerminal: false,
            preferences: preferences
        ) == TerminalBackingColor(
            red: 20.0 / 255,
            green: 20.0 / 255,
            blue: 22.0 / 255
        ))
        #expect(DetailCanvas.backingColor(
            showsTerminal: true,
            preferences: preferences
        ) == TerminalBackingColor(red: 1, green: 0, blue: 0))
    }
}
