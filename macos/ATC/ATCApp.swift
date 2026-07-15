import SwiftUI

@main
struct ATCApp: App {
    @State private var appModel = AppModel()
    /// Single-window today; one WindowState per window is the seam a
    /// future multi-window pass builds on.
    @State private var windowState = WindowState()
    @State private var keyboardConfigStore: KeyboardConfigStore

    init() {
        let store = KeyboardConfigStore()
        store.loadAtLaunch()
        _keyboardConfigStore = State(initialValue: store)
    }

    var body: some Scene {
        WindowGroup {
            RootView(configStore: keyboardConfigStore)
                .environment(appModel)
                .environment(windowState)
                .environment(keyboardConfigStore)
                .preferredColorScheme(.dark)
        }
        .commands {
            AppCommands(
                appModel: appModel,
                windowState: windowState,
                configStore: keyboardConfigStore
            )
        }
        Settings {
            SettingsView()
                .environment(appModel)
                .preferredColorScheme(.dark)
        }
    }
}
