import SwiftUI

/// App Settings window. macOS owns the menu item and standard keyboard shortcut.
struct SettingsView: View {
    /// One window size for every tab so switching tabs never resizes.
    static let windowSize = CGSize(width: 720, height: 500)

    var body: some View {
        TabView {
            Tab("General", systemImage: "gearshape") {
                GeneralSettingsView()
                    .frame(width: Self.windowSize.width, height: Self.windowSize.height)
            }
            Tab("Connections", systemImage: "network") {
                ConnectionsSettingsView()
                    .frame(width: Self.windowSize.width, height: Self.windowSize.height)
            }
            Tab("Notifications", systemImage: "bell") {
                NotificationsSettingsView()
                    .frame(width: Self.windowSize.width, height: Self.windowSize.height)
            }
        }
    }
}

// Previews are compiled into Release builds too; the fixtures they use
// are not.
#if DEBUG

    #Preview("Settings") {
        SettingsView()
            .environment(AppModel.preview())
            .preferredColorScheme(.dark)
    }

#endif
