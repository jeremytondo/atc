import AppKit
import SwiftUI
import Testing
import AtelierCodeAPI
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

    /// The runtime's own poll task races a manual `refreshAll` (the stores'
    /// generation guard drops the loser), so wait boundedly for first data
    /// instead of asserting right after one refresh.
    private func waitForData(_ runtime: ConnectionRuntime) async {
        for _ in 0..<100 {
            if !runtime.projects.projects.isEmpty && !runtime.sessions.sessions.isEmpty { return }
            try? await Task.sleep(for: .milliseconds(20))
        }
    }

    @Test("sidebar renders projects with nested sessions without crashing")
    func hostSidebar() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        #expect(!runtime.projects.projects.isEmpty)
        #expect(!runtime.sessions.sessions.isEmpty)
        host(
            ProjectSidebarView(
                selection: .constant(nil),
                searchText: "",
                connectedRefs: [],
                newSessionContext: .constant(nil)
            )
            .environment(appModel),
            width: 280, height: 520
        )
    }

    @Test("sidebar renders filtered by search without crashing")
    func hostSidebarSearching() async throws {
        let appModel = AppModel.preview()
        if let runtime = appModel.runtimes.first { await waitForData(runtime) }
        host(
            ProjectSidebarView(
                selection: .constant(nil),
                searchText: "atelier",
                connectedRefs: [],
                newSessionContext: .constant(nil)
            )
            .environment(appModel),
            width: 280, height: 520
        )
    }

    @Test("sidebar renders the no-connections empty state without crashing")
    func hostSidebarNoConnections() async throws {
        let suite = "ProjectUIHostingSmokeTest.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let appModel = AppModel(
            connections: ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore()),
            clientFactory: { _ in MockAtelierCodeClient() }
        )
        #expect(appModel.runtimes.isEmpty)
        host(
            ProjectSidebarView(
                selection: .constant(nil),
                searchText: "",
                connectedRefs: [],
                newSessionContext: .constant(nil)
            )
            .environment(appModel),
            width: 280, height: 520
        )
    }

    @Test("create-project sheet hosts without crashing")
    func hostCreateProject() async throws {
        host(
            CreateProjectSheet()
                .environment(AppModel.preview())
        )
    }

    @Test("create-project sheet with two connections hosts without crashing")
    func hostCreateProjectMultiConnection() async throws {
        host(
            CreateProjectSheet()
                .environment(AppModel.preview(connections: [
                    (name: "Workstation", client: MockAtelierCodeClient()),
                    (name: "Laptop", client: MockAtelierCodeClient()),
                ]))
        )
    }

    @Test("create-session sheet loads actions without crashing")
    func hostCreateSession() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let project = try #require(runtime.projects.projects.first)
        host(
            CreateSessionSheet(context: NewSessionContext(
                connectionID: runtime.id, project: project
            ))
            .environment(appModel)
        )
    }
}
