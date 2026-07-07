import SwiftUI
import CockpitAPI

/// Metadata view for non-attachable sessions, and the inspector content
/// for live ones.
struct SessionDetailView: View {
    @Environment(AppModel.self) private var appModel
    let session: Session

    @State private var detail: SessionDetail?

    var body: some View {
        Form {
            Section("Session") {
                LabeledContent("Name", value: session.displayName)
                LabeledContent("ID", value: session.id)
                LabeledContent("Status") {
                    StatusBadge(session: session, showLabel: true)
                }
                LabeledContent("Action", value: session.action)
                LabeledContent("Environment", value: session.environment)
                if let project = session.project {
                    LabeledContent("Project", value: project.name)
                }
                LabeledContent("Working Directory", value: session.workingDir)
            }
            if session.failureReason != nil || session.failureCode != nil {
                Section("Failure") {
                    if let code = session.failureCode {
                        LabeledContent("Code", value: code)
                    }
                    if let reason = session.failureReason {
                        LabeledContent("Reason", value: reason)
                    }
                }
            }
            Section("Timestamps") {
                LabeledContent("Created", value: session.createdAt.formatted(date: .abbreviated, time: .shortened))
                LabeledContent("Updated", value: session.updatedAt.formatted(date: .abbreviated, time: .shortened))
                if let terminatedAt = session.terminatedAt {
                    LabeledContent("Terminated", value: terminatedAt.formatted(date: .abbreviated, time: .shortened))
                }
                if let archivedAt = session.archivedAt {
                    LabeledContent("Archived", value: archivedAt.formatted(date: .abbreviated, time: .shortened))
                }
            }
            if let detail {
                if let prompt = detail.prompt, !prompt.isEmpty {
                    Section("Prompt") {
                        Text(prompt)
                            .font(.callout)
                            .textSelection(.enabled)
                    }
                }
                if let params = detail.params, !params.isEmpty {
                    Section("Parameters") {
                        ForEach(params.keys.sorted(), id: \.self) { key in
                            LabeledContent(key, value: params[key]?.displayString ?? "")
                        }
                    }
                }
            }
        }
        .formStyle(.grouped)
        .task(id: session.id) {
            detail = try? await appModel.client.session(id: session.id)
        }
    }
}
