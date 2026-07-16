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
}
