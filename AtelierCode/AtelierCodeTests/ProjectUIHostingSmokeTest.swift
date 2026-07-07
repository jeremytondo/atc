import AppKit
import SwiftUI
import Testing
import CockpitAPI
@testable import AtelierCode

/// Hosts the project-first sidebar and the create sheets in a real window
/// and pumps the run loop — catches List/DisclosureGroup diff crashes
/// previews can't attribute (same rationale as PickerHostingSmokeTest).
@Suite("Project UI hosting smoke")
struct ProjectUIHostingSmokeTest {
    private func pump(seconds: TimeInterval) {
        RunLoop.main.run(until: Date(timeIntervalSinceNow: seconds))
    }

    private func host(_ view: some View, width: CGFloat = 460, height: CGFloat = 480) {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: width, height: height),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(rootView: view)
        window.orderFront(nil)
        pump(seconds: 0.5)
        window.orderOut(nil)
    }

    @Test("sidebar renders projects with nested sessions without crashing")
    func hostSidebar() async throws {
        let appModel = AppModel(client: MockCockpitClient())
        await appModel.projects.refresh()
        await appModel.sessions.refresh()
        #expect(!appModel.projects.projects.isEmpty)
        #expect(!appModel.sessions.sessions.isEmpty)
        host(
            ProjectSidebarView(
                selection: .constant(nil),
                searchText: "",
                connectedIDs: [],
                newSessionProject: .constant(nil)
            )
            .environment(appModel),
            width: 280, height: 520
        )
    }

    @Test("sidebar renders filtered by search without crashing")
    func hostSidebarSearching() async throws {
        let appModel = AppModel(client: MockCockpitClient())
        await appModel.projects.refresh()
        await appModel.sessions.refresh()
        host(
            ProjectSidebarView(
                selection: .constant(nil),
                searchText: "atelier",
                connectedIDs: [],
                newSessionProject: .constant(nil)
            )
            .environment(appModel),
            width: 280, height: 520
        )
    }

    @Test("create-project sheet hosts without crashing")
    func hostCreateProject() async throws {
        host(
            CreateProjectSheet()
                .environment(AppModel(client: MockCockpitClient()))
        )
    }

    @Test("create-session sheet loads actions without crashing")
    func hostCreateSession() async throws {
        let appModel = AppModel(client: MockCockpitClient())
        await appModel.projects.refresh()
        let project = try #require(appModel.projects.projects.first)
        host(
            CreateSessionSheet(project: project)
                .environment(appModel)
        )
    }
}
