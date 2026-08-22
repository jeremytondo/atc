import ATCAppServerAPI
import SwiftUI

/// The General settings section: app-wide defaults that belong to no one
/// Connection. The kind a new thread pre-selects is a client preference
/// (the server has no default; the sheet's control decides per thread).
struct GeneralSettingsView: View {
    /// The `@AppStorage` key the New Thread sheet reads.
    static let newThreadKindKey = "newThreadDefaultKind"

    @AppStorage(Self.newThreadKindKey) private var newThreadKind: ThreadKind = .chat

    var body: some View {
        Form {
            Section {
                Picker("New threads open as", selection: $newThreadKind) {
                    ForEach(ThreadKind.allCases, id: \.self) { kind in
                        Text(kind.displayName).tag(kind)
                    }
                }
            } footer: {
                Text(
                    "Chat threads are driven from atc's own chat; TUI threads run the agent's terminal UI inside atc. A thread keeps its kind for life."
                )
                .font(.caption)
                .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
    }
}

#if DEBUG

    #Preview("General") {
        GeneralSettingsView()
            .frame(width: 700, height: 450)
            .preferredColorScheme(.dark)
    }

#endif
