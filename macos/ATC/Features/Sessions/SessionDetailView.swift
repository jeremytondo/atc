import SwiftUI
import ATCAPI

/// Session metadata and inspector content.
struct SessionDetailView: View {
    @Environment(AppModel.self) private var appModel
    let sessionRef: SessionRef
    let session: Session
    /// The owning Connection's client — details load from the same server
    /// the session lives on.
    let client: any ATCClient

    @State private var detail: SessionDetail?

    var body: some View {
        Form {
            Section("Session") {
                LabeledContent("Name", value: session.displayName)
                LabeledContent("ID", value: session.id)
                LabeledContent("Status") {
                    StatusBadge(session: session, showLabel: true)
                }
                LabeledContent("Action", value: session.actionLabel)
                LabeledContent("Environment", value: session.environment)
                if let project = session.project {
                    LabeledContent("Project", value: project.name)
                }
                LabeledContent("Working Directory", value: session.workingDir)
            }
            Section("Timestamps") {
                LabeledContent("Created", value: session.createdAt.formatted(date: .abbreviated, time: .shortened))
                LabeledContent("Updated", value: session.updatedAt.formatted(date: .abbreviated, time: .shortened))
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
            do {
                detail = try await client.session(id: session.id)
            } catch {
                _ = appModel.handleSessionInteractionError(
                    error,
                    connectionID: sessionRef.connectionID
                )
            }
        }
    }
}
