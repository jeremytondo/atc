import SwiftUI

/// App Settings window. macOS owns the menu item and standard keyboard shortcut.
struct SettingsView: View {
    /// One window size for every tab so switching tabs never resizes.
    static let windowSize = CGSize(width: 720, height: 500)

    var body: some View {
        TabView {
            Tab("Connections", systemImage: "network") {
                ConnectionsSettingsView()
                    .frame(width: Self.windowSize.width, height: Self.windowSize.height)
            }
            Tab("Actions", systemImage: "bolt") {
                ActionsSettingsView()
                    .frame(width: Self.windowSize.width, height: Self.windowSize.height)
            }
        }
    }
}

#Preview("Settings") {
    let store = ConnectionsStore(defaults: UserDefaults(suiteName: "preview.settings.connections")!, credentials: InMemoryCredentialStore())
    _ = try? store.add(name: "Workstation", urlString: "http://workstation.tail1f9a09.ts.net:7331", token: "")
    _ = try? store.add(name: "Local Dev", urlString: "http://127.0.0.1:7331", token: "")
    return SettingsView()
        .environment(AppModel(connections: store, clientFactory: { _ in MockATCClient() }))
        .preferredColorScheme(.dark)
}
