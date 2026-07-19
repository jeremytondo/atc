import SwiftUI

@main
struct ATCApp: App {
    @State private var appModel = AppModel()
    /// Single-window today; one WindowState per window is the seam a
    /// future multi-window pass builds on.
    @State private var windowState = WindowState()
    @State private var configurationStore: ConfigurationStore

    init() {
        let store = ConfigurationStore()
        store.loadAtLaunch()
        _configurationStore = State(initialValue: store)
    }

    var body: some Scene {
        WindowGroup {
            RootView(configStore: configurationStore)
                .environment(appModel)
                .environment(windowState)
                .environment(configurationStore)
                .preferredColorScheme(.dark)
        }
        .commands {
            AppCommands(
                appModel: appModel,
                windowState: windowState,
                configStore: configurationStore
            )
        }
        Settings {
            SettingsView()
                .environment(appModel)
                .preferredColorScheme(.dark)
        }
    }
}
