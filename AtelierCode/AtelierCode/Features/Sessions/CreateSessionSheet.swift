import SwiftUI
import CockpitAPI

/// Form for `POST /sessions/start`, always scoped to a project: pick an
/// action, optionally name the session. Working directory and environment
/// are inherited (project directory, server-default environment).
struct CreateSessionSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    let project: Project
    /// Called with the new session's ID so the shell can select it.
    var onCreated: (String) -> Void = { _ in }

    @State private var actions: [CockpitAction] = []
    @State private var loadError: String?

    @State private var selectedActionName = ""
    @State private var name = ""

    @State private var isSubmitting = false
    @State private var submitError: String?

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
        !isSubmitting && selectedAction != nil
    }

    private func loadActions() async {
        do {
            actions = try await appModel.client.actions()
            loadError = nil
            if selectedActionName.isEmpty {
                selectedActionName = actions.first(where: \.enabled)?.name ?? ""
            }
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func submit() async {
        guard let action = selectedAction else { return }
        isSubmitting = true
        defer { isSubmitting = false }

        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        let request = StartSessionRequest(
            action: action.name,
            projectId: project.id,
            name: trimmedName.isEmpty ? nil : trimmedName
        )

        do {
            let detail = try await appModel.sessions.start(request)
            submitError = nil
            dismiss()
            onCreated(detail.id)
        } catch {
            submitError = error.localizedDescription
        }
    }
}

#Preview {
    CreateSessionSheet(project: Project(
        id: "prj_atelier",
        name: "Atelier",
        workingDir: "/home/dev/Projects/atelier",
        createdAt: .now,
        updatedAt: .now
    ))
    .environment(AppModel(client: MockCockpitClient()))
    .preferredColorScheme(.dark)
}
