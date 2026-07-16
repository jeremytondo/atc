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
        let store = KeyboardConfigStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let appModel = AppModel.preview()
        let windowState = WindowState.ephemeral()
        let router = WindowKeyboardRouter(
            keymap: store.keymap,
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

    @Test("routing container mounts the presented palette without crashing")
    func hostIntegratedPalette() {
        let store = KeyboardConfigStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let appModel = AppModel.preview()
        let windowState = WindowState.ephemeral()
        windowState.isCommandPalettePresented = true
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
        #expect(windowState.isCommandPalettePresented)
        window.orderOut(nil)
    }

    @Test("palette dismissal restores a valid previous responder")
    func restoresPreviousResponder() {
        let store = KeyboardConfigStore(
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

        windowState.isCommandPalettePresented = true
        pump(until: { window.firstResponder !== previousResponder })
        #expect(window.firstResponder !== previousResponder)

        windowState.isCommandPalettePresented = false
        pump(until: { window.firstResponder === previousResponder })
        #expect(window.firstResponder === previousResponder)
        window.orderOut(nil)
    }
}

private final class FocusProbeView: NSView {
    override var acceptsFirstResponder: Bool { true }
}
