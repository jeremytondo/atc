import SwiftUI

/// App Settings window. macOS owns the menu item and standard keyboard shortcut.
struct SettingsView: View {
    var body: some View {
        TabView {
            Tab("Connections", systemImage: "network") {
                ConnectionsSettingsView()
                    .frame(width: 700, height: 450)
            }
            Tab("Actions", systemImage: "bolt") {
                ActionsSettingsView()
                    .frame(width: 760, height: 540)
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
