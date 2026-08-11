import ATCAppServerAPI
import SwiftUI

/// Read-only detail for the open thread. Everything shown here comes straight
/// from the server record; the inspector never mutates.
struct ThreadInspectorView: View {
    let thread: ATCThread
    let projectName: String

    var body: some View {
        Form {
            Section {
                LabeledContent("Thread", value: thread.displayName)
                LabeledContent("Project", value: projectName)
                LabeledContent("Agent") {
                    Label(thread.agentId.displayName, systemImage: thread.agentId.systemImage)
                }
            }
            Section {
                LabeledContent("Working Directory") {
                    Text(thread.workingDirectory)
                        .font(.callout.monospaced())
                        .textSelection(.enabled)
                }
                LabeledContent("Activity", value: thread.activityState.detailLabel)
            }
            Section {
                LabeledContent("Created", value: thread.createdAt.formatted(date: .abbreviated, time: .shortened))
                if let pinnedAt = thread.pinnedAt {
                    LabeledContent("Pinned", value: pinnedAt.formatted(date: .abbreviated, time: .shortened))
                }
                if let archivedAt = thread.archivedAt {
                    LabeledContent("Archived", value: archivedAt.formatted(date: .abbreviated, time: .shortened))
                }
            }
        }
        .formStyle(.grouped)
    }
}
