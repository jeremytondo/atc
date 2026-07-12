import SwiftUI

@main
struct ATCApp: App {
    @State private var appModel = AppModel()
    /// Single-window today; one WindowState per window is the seam a
    /// future multi-window pass builds on.
    @State private var windowState = WindowState()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(appModel)
                .environment(windowState)
                .preferredColorScheme(.dark)
        }
        .commands {
            AppCommands(appModel: appModel, windowState: windowState)
        }
        Settings {
            SettingsView()
                .environment(appModel)
                .preferredColorScheme(.dark)
        }
    }
}
