import SwiftUI
import CockpitAPI

/// Sidebar: sessions grouped by status, archived behind the filter toggle.
struct SessionListView: View {
    @Environment(AppModel.self) private var appModel
    @Binding var selection: String?
    let searchText: String
    let connectedIDs: Set<String>

    private static let groupOrder: [SessionStatus] = [.running, .starting, .failed, .terminated]

    var body: some View {
        let store = appModel.sessions
        List(selection: $selection) {
            ForEach(Self.groupOrder, id: \.self) { status in
                let group = filtered.filter { $0.status == status && !$0.isArchived }
                if !group.isEmpty {
                    Section(status.rawValue.capitalized) {
                        ForEach(group) { session in
                            SessionRowView(session: session, isConnected: connectedIDs.contains(session.id))
                                .tag(session.id)
                        }
                    }
                }
            }
            let archived = filtered.filter(\.isArchived)
            if !archived.isEmpty {
                Section("Archived") {
                    ForEach(archived) { session in
                        SessionRowView(session: session, isConnected: false)
                            .tag(session.id)
                    }
                }
            }
        }
        .overlay {
            if filtered.isEmpty && store.hasLoadedOnce {
                if let error = store.lastError {
                    ContentUnavailableView {
                        Label("Can't Reach Cockpit", systemImage: "wifi.exclamationmark")
                    } description: {
                        Text(error)
                    } actions: {
                        Button("Retry") { Task { await store.refresh() } }
                    }
                } else {
                    ContentUnavailableView(
                        searchText.isEmpty ? "No Sessions" : "No Matches",
                        systemImage: "rectangle.stack",
                        description: Text(searchText.isEmpty
                            ? "Create a session to get started."
                            : "No sessions match “\(searchText)”.")
                    )
                }
            }
        }
    }

    private var filtered: [Session] {
        let all = appModel.sessions.sessions
        guard !searchText.isEmpty else { return all }
        return all.filter {
            $0.displayName.localizedCaseInsensitiveContains(searchText)
                || $0.workingDir.localizedCaseInsensitiveContains(searchText)
                || $0.action.localizedCaseInsensitiveContains(searchText)
        }
    }
}
