import AppKit
import Foundation
import SwiftUI
import Testing
@testable import ATC

@MainActor
@Suite("Command palette hosting smoke")
struct CommandPaletteHostingSmokeTest {
    @Test("palette hosts with preview fixtures without crashing")
    func hostPalette() {
        let store = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let appModel = AppModel.preview()
        let windowState = WindowState.ephemeral()
        let router = WindowKeyboardRouter(
            keymap: store.configuration.keymap,
            context: CommandContext(
                appModel: appModel,
                windowState: windowState,
                configStore: store
            )
        )
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(
            rootView: CommandPaletteView()
                .environment(appModel)
                .environment(windowState)
                .environment(store)
                .environment(router)
        )
        window.makeKeyAndOrderFront(nil)
        pump(seconds: 0.5)
        window.orderOut(nil)
    }

    @Test("palette hosts heterogeneous results for an Active Workspace")
    func hostHeterogeneousPalette() async throws {
        let store = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let appModel = AppModel.preview()
        await appModel.refreshAll()
        let windowState = WindowState.ephemeral()
        let connectionID = try #require(appModel.runtimes.first?.id)
        #expect(windowState.activateWorkspace(
            WorkspaceRef(connectionID: connectionID, workspaceID: "wsp_parser"),
            in: appModel
        ))
        let router = WindowKeyboardRouter(
            keymap: store.configuration.keymap,
            context: CommandContext(
                appModel: appModel,
                windowState: windowState,
                configStore: store
            )
        )
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(
            rootView: CommandPaletteView(initialQuery: "parser")
                .environment(appModel)
                .environment(windowState)
                .environment(store)
                .environment(router)
        )
        window.makeKeyAndOrderFront(nil)
        pump(seconds: 0.5)
        window.orderOut(nil)
    }

    @Test("scoped palettes host their blank-query listings without crashing")
    func hostScopedPalettes() async throws {
        let store = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let appModel = AppModel.preview()
        await appModel.refreshAll()
        let windowState = WindowState.ephemeral()
        let connectionID = try #require(appModel.runtimes.first?.id)
        #expect(windowState.activateWorkspace(
            WorkspaceRef(connectionID: connectionID, workspaceID: "wsp_parser"),
            in: appModel
        ))
        let router = WindowKeyboardRouter(
            keymap: store.configuration.keymap,
            context: CommandContext(
                appModel: appModel,
                windowState: windowState,
                configStore: store
            )
        )
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        for presentation in [
            CommandPalettePresentation.sessions, .terminals, .workspaces,
        ] {
            windowState.commandPalettePresentation = presentation
            window.contentView = NSHostingView(
                rootView: CommandPaletteView()
                    .environment(appModel)
                    .environment(windowState)
                    .environment(store)
                    .environment(router)
            )
            window.makeKeyAndOrderFront(nil)
            pump(seconds: 0.2)
            #expect(windowState.commandPalettePresentation == presentation)
        }
        window.orderOut(nil)
    }

    @Test("routing container mounts the presented palette without crashing")
    func hostIntegratedPalette() {
        let store = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let appModel = AppModel.preview()
        let windowState = WindowState.ephemeral()
        windowState.commandPalettePresentation = .all
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(
            rootView: KeyboardRoutingContainer(
                appModel: appModel,
                windowState: windowState,
                configStore: store
            ) {
                Color.clear
            }
            .environment(appModel)
            .environment(windowState)
        )
        window.makeKeyAndOrderFront(nil)
        pump(seconds: 0.5)
        #expect(windowState.commandPalettePresentation == .all)
        window.orderOut(nil)
    }

    @Test("palette dismissal restores a valid previous responder")
    func restoresPreviousResponder() {
        let store = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let appModel = AppModel.preview()
        let windowState = WindowState.ephemeral()
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        let hostingView = NSHostingView(
            rootView: KeyboardRoutingContainer(
                appModel: appModel,
                windowState: windowState,
                configStore: store
            ) {
                Color.clear
            }
            .environment(appModel)
            .environment(windowState)
        )
        window.contentView = hostingView
        window.makeKeyAndOrderFront(nil)
        pump(seconds: 0.2)

        let previousResponder = FocusProbeView(frame: .zero)
        hostingView.addSubview(previousResponder)
        #expect(window.makeFirstResponder(previousResponder))

        windowState.commandPalettePresentation = .all
        pump(until: { window.firstResponder !== previousResponder })
        #expect(window.firstResponder !== previousResponder)

        windowState.commandPalettePresentation = nil
        pump(until: { window.firstResponder === previousResponder })
        #expect(window.firstResponder === previousResponder)
        window.orderOut(nil)
    }

    @Test("deactivation before the palette mounts restores the stashed responder")
    func restoresResponderWhenDeactivationBeatsMounting() {
        let store = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let appModel = AppModel.preview()
        let windowState = WindowState.ephemeral()
        let router = WindowKeyboardRouter(
            keymap: store.configuration.keymap,
            context: CommandContext(
                appModel: appModel,
                windowState: windowState,
                configStore: store
            )
        )
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        let probe = FocusProbeView(frame: .zero)
        window.contentView?.addSubview(probe)
        window.makeKeyAndOrderFront(nil)
        #expect(window.makeFirstResponder(probe))

        let coordinator = KeyboardMonitorHost.Coordinator(
            router: router,
            onDeactivate: { windowState.commandPalettePresentation = nil },
            focusFallback: {}
        )
        coordinator.install(for: window)

        // Simulate the keyboard opener mid-flight: suspension has flipped and
        // the key monitor stashed the responder and cleared focus, but the
        // palette's window accessor has not mounted yet.
        windowState.commandPalettePresentation = .all
        router.responderBeforeSuspension = probe
        window.makeFirstResponder(nil)

        NotificationCenter.default.post(
            name: NSWindow.didResignKeyNotification,
            object: window
        )
        pump(until: { window.firstResponder === probe })

        #expect(windowState.commandPalettePresentation == nil)
        #expect(window.firstResponder === probe)
        #expect(router.responderBeforeSuspension == nil)
        coordinator.stop()
        window.orderOut(nil)
    }
}

private final class FocusProbeView: NSView {
    override var acceptsFirstResponder: Bool { true }
}
