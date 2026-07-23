import SwiftUI
import ATCAPI

/// Session metadata and inspector content.
struct SessionDetailView: View {
    let session: Session

    var body: some View {
        let identity = SessionIdentity(session: session)
        Form {
            Section("Session") {
                LabeledContent("Identity", value: identity.indexedLabel)
                if let customName = identity.customName {
                    LabeledContent("Custom Name", value: customName)
                }
                LabeledContent("ID", value: session.id)
                LabeledContent("Status") {
                    StatusBadge(session: session, showLabel: true)
                }
                LabeledContent("Action", value: session.actionName ?? "Interactive Shell")
                LabeledContent("Agent", value: session.isAgent ? "Yes" : "No")
                if let project = session.project {
                    LabeledContent("Project", value: project.name)
                }
                LabeledContent("Working Directory", value: session.workingDir)
            }
            Section("Timestamps") {
                LabeledContent("Created", value: session.createdAt.formatted(date: .abbreviated, time: .shortened))
                LabeledContent("Updated", value: session.updatedAt.formatted(date: .abbreviated, time: .shortened))
            }
        }
        .formStyle(.grouped)
    }
}
