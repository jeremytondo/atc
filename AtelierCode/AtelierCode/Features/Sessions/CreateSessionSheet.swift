import SwiftUI
import CockpitAPI

/// Form for `POST /sessions/start`, always scoped to a project on one
/// Connection: pick an action, optionally name the session. Action loading,
/// the start request, and the follow-up attach all use the owning runtime.
struct CreateSessionSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    let context: NewSessionContext
    /// Called with the new session's ref so the shell can select it.
    var onCreated: (SessionRef) -> Void = { _ in }

    @State private var actions: [CockpitAction] = []
    @State private var loadError: String?

    @State private var selectedActionName = ""
    @State private var name = ""

    @State private var isSubmitting = false
    @State private var submitError: String?

    private var project: Project { context.project }

    private var runtime: ConnectionRuntime? {
        appModel.runtime(id: context.connectionID)
    }

    private var selectedAction: CockpitAction? {
        actions.first { $0.name == selectedActionName }
    }

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section {
                    Picker("Action", selection: $selectedActionName) {
                        ForEach(actions.filter(\.enabled)) { action in
                            Text(action.displayLabel).tag(action.name)
                        }
                    }
                    TextField("Name (optional)", text: $name)
                } header: {
                    Label {
                        Text(project.name)
                    } icon: {
                        Image(systemName: "folder")
                    }
                    .font(.headline)
                } footer: {
                    Text(project.workingDir)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.head)
                }

                if let message = submitError ?? loadError {
                    Section {
                        Label(message, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
                            .font(.callout)
                    }
                }
            }
            .formStyle(.grouped)

            Divider()
            HStack {
                Button("Cancel", role: .cancel) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Spacer()
                Button {
                    Task { await submit() }
                } label: {
                    if isSubmitting {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Start Session")
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(!canSubmit)
            }
            .padding(12)
        }
        .frame(width: 400, height: 260)
        .task { await loadActions() }
    }

    private var canSubmit: Bool {
        !isSubmitting && selectedAction != nil && runtime != nil
    }

    private func loadActions() async {
        guard let runtime else {
            loadError = "This project's connection is no longer configured."
            return
        }
        do {
            actions = try await runtime.client.actions()
            loadError = nil
            if selectedActionName.isEmpty {
                selectedActionName = actions.first(where: \.enabled)?.name ?? ""
            }
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func submit() async {
        guard let action = selectedAction, let runtime else { return }
        isSubmitting = true
        defer { isSubmitting = false }

        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        let request = StartSessionRequest(
            action: action.name,
            projectId: project.id,
            name: trimmedName.isEmpty ? nil : trimmedName
        )

        do {
            let detail = try await runtime.sessions.start(request)
            submitError = nil
            dismiss()
            onCreated(SessionRef(connectionID: runtime.id, sessionID: detail.id))
        } catch {
            submitError = error.localizedDescription
        }
    }
}

#Preview {
    let appModel = AppModel.preview()
    CreateSessionSheet(context: NewSessionContext(
        connectionID: appModel.runtimes.first!.id,
        project: Project(
            id: "prj_atelier",
            name: "Atelier",
            workingDir: "/home/dev/Projects/atelier",
            createdAt: .now,
            updatedAt: .now
        )
    ))
    .environment(appModel)
    .preferredColorScheme(.dark)
}
