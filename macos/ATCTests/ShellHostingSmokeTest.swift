import AppKit
import SwiftUI
import Testing
@testable import ATC

/// Hosts the window the app actually boots into, in a real NSWindow, and
/// pumps the run loop. Launch-time AppKit and SwiftUI warnings surface here
/// against a controlled model instead of live user state.
@MainActor
@Suite("Shell hosting smoke")
struct ShellHostingSmokeTest {
    private func host(_ view: some View) {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1_100, height: 700),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(rootView: view)
        window.orderFront(nil)
        pump(seconds: 0.5)
        window.orderOut(nil)
    }

    private func rootView(_ appModel: AppModel, _ windowState: WindowState) -> some View {
        // Mirror WindowRootView's per-window wiring: RootView's descendants
        // (sidebar, palette, feedback overlay) read the window's router
        // from the environment.
        let configStore = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        )
        return RootView(configStore: configStore)
            .environment(appModel)
            .environment(windowState)
            .environment(WindowKeyboardRouter.forWindow(
                appModel: appModel,
                windowState: windowState,
                configStore: configStore
            ))
    }

    @Test("the root view hosts the Dashboard with seeded data without crashing")
    func hostRootOnDashboard() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await settle(until: { !runtime.threads.threads.isEmpty })

        let windowState = WindowState()
        // Launch behavior: Dashboard, no restored selection, filter on All.
        #expect(windowState.selectedContent == .dashboard)
        #expect(windowState.threadFilter == .all)
        host(rootView(appModel, windowState))
    }

    @Test("the root view hosts before any data has arrived")
    func hostRootBeforeData() {
        // The app's real launch order: the window is up before the first
        // resync returns, then rows insert into the live sidebar.
        host(rootView(AppModel.preview(), WindowState()))
    }

    @Test("the root view hosts a selected thread without crashing")
    func hostRootWithSelectedThread() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await settle(until: { !runtime.threads.threads.isEmpty })
        let thread = try #require(runtime.threads.threads.first)

        let windowState = WindowState()
        await windowState.openThread(
            ThreadRef(connectionID: runtime.id, threadID: thread.id),
            in: appModel
        )
        host(rootView(appModel, windowState))
    }
}
