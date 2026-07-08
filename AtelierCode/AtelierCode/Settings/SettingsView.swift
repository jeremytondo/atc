import SwiftUI

/// Sidebar-style, multi-section Settings shell. v1 has a single `Connections`
/// section; the enum keeps room for more without reshaping the window.
struct SettingsView: View {
    private enum SettingsSection: Hashable, CaseIterable, Identifiable {
        case connections

        var id: Self { self }
        var label: String {
            switch self {
            case .connections: return "Connections"
            }
        }
        var systemImage: String {
            switch self {
            case .connections: return "network"
            }
        }
    }

    @State private var selection: SettingsSection? = .connections

    var body: some View {
        NavigationSplitView {
            List(SettingsSection.allCases, selection: $selection) { section in
                Label(section.label, systemImage: section.systemImage)
                    .tag(section)
            }
            .navigationSplitViewColumnWidth(min: 160, ideal: 180, max: 220)
        } detail: {
            switch selection {
            case .connections, .none:
                ConnectionsSettingsView()
            }
        }
        .frame(width: 700, height: 450)
    }
}

#Preview("Settings") {
    let store = ConnectionsStore(defaults: UserDefaults(suiteName: "preview.settings.connections")!)
    _ = try? store.add(name: "Workstation", urlString: "http://workstation.tail1f9a09.ts.net:7331", token: "")
    _ = try? store.add(name: "Local Dev", urlString: "http://127.0.0.1:7331", token: "")
    return SettingsView()
        .environment(AppModel(client: MockCockpitClient(), connections: store))
        .preferredColorScheme(.dark)
}
