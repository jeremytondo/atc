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
        }
    }
}

#Preview("Settings") {
    SettingsView()
        .environment(AppModel.preview())
        .preferredColorScheme(.dark)
}
