import SwiftUI
import CockpitAPI

/// Single-window shell: sessions sidebar, session content on the right.
struct ContentView: View {
    @Environment(AppModel.self) private var appModel
    @State private var selectedSessionID: String?
    @State private var searchText = ""

    var body: some View {
        @Bindable var store = appModel.sessions
        NavigationSplitView {
            SessionListView(
                selection: $selectedSessionID,
                searchText: searchText,
                connectedIDs: []
            )
            .navigationSplitViewColumnWidth(min: 220, ideal: 280)
            .searchable(text: $searchText, placement: .sidebar, prompt: "Search sessions")
            .toolbar {
                ToolbarItemGroup {
                    Toggle(isOn: $store.includeArchived) {
                        Label("Show Archived", systemImage: "archivebox")
                    }
                    .help("Show archived sessions")
                    Button {
                        Task { await store.refresh() }
                    } label: {
                        Label("Refresh", systemImage: "arrow.clockwise")
                    }
                    .help("Refresh sessions")
                }
            }
        } detail: {
            if let session = selectedSessionID.flatMap({ appModel.sessions.session(id: $0) }) {
                SessionContentView(session: session)
            } else {
                ContentUnavailableView(
                    "No Session Selected",
                    systemImage: "terminal",
                    description: Text("Select a session in the sidebar.")
                )
            }
        }
        .task {
            await appModel.sessions.pollLoop()
        }
    }
}

/// Content area for the selected session: compact header above either the
/// terminal (attachable) or a metadata view.
struct SessionContentView: View {
    @Environment(AppModel.self) private var appModel
    let session: Session

    var body: some View {
        VStack(spacing: 0) {
            SessionHeaderBar(session: session)
            Divider()
            if session.attachable {
                // Phase 4 replaces this placeholder with the live terminal pane.
                ContentUnavailableView(
                    "Terminal Coming Soon",
                    systemImage: "terminal",
                    description: Text("Attach lands in Phase 4.")
                )
            } else {
                SessionDetailView(session: session)
            }
        }
        .navigationTitle(session.displayName)
    }
}

/// Compact header: name, status, session actions.
struct SessionHeaderBar: View {
    @Environment(AppModel.self) private var appModel
    let session: Session

    var body: some View {
        HStack(spacing: 12) {
            StatusBadge(session: session, showLabel: true)
            Text(session.displayName)
                .font(.headline)
                .lineLimit(1)
            Text(session.workingDir)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.head)
            Spacer()
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }
}

#Preview {
    ContentView()
        .environment(AppModel(client: MockCockpitClient()))
        .preferredColorScheme(.dark)
}
