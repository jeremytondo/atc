import SwiftUI

@main
struct ATCApp: App {
    @State private var appModel = AppModel()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environment(appModel)
                .preferredColorScheme(.dark)
        }
        Settings {
            SettingsView()
                .environment(appModel)
                .preferredColorScheme(.dark)
        }
    }
}
