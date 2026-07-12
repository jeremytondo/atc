import SwiftUI
import ATCAPI

/// Form for `POST /sessions/start`, always scoped to a project on one
/// Connection: pick a workspace (or name a new one), pick an action, and
/// optionally name the session. Action/workspace loading, the start request,
/// and the follow-up attach all use the owning runtime.
struct CreateSessionSheet: View {
    /// Sentinel Picker tag for "create a new workspace instead".
    private static let newWorkspaceTag = ""

    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    let context: NewSessionContext
    /// Called with the new session's ref so the shell can select it.
    var onCreated: (SessionRef) -> Void = { _ in }

    @State private var actions: [ATCAction] = []
    @State private var workspaces: [Workspace] = []
    @State private var loadError: String?

    @State private var selectedActionName = ""
    @State private var selectedWorkspaceID = Self.newWorkspaceTag
    @State private var newWorkspaceName = ""
    @State private var name = ""

    @State private var isSubmitting = false
    @State private var submitError: String?

    private var project: Project { context.project }

    private var runtime: ConnectionRuntime? {
        appModel.runtime(id: context.connectionID)
    }

    private var selectedAction: ATCAction? {
        actions.first { $0.name == selectedActionName }
    }

    private var enabledActions: [ATCAction] {
        actions.filter(\.enabled)
    }

    private var isCreatingWorkspace: Bool {
        selectedWorkspaceID == Self.newWorkspaceTag
    }

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section {
                    Picker("Workspace", selection: $selectedWorkspaceID) {
                        ForEach(workspaces) { workspace in
                            Text(workspace.name).tag(workspace.id)
                        }
                        Text("New Workspace…").tag(Self.newWorkspaceTag)
                    }
                    if isCreatingWorkspace {
                        TextField("New workspace name", text: $newWorkspaceName)
                    }
                    Picker("Action", selection: $selectedActionName) {
                        // Actions load async; the "" selection needs a matching
                        // tag until then or AppKit logs an invalid selection.
                        if enabledActions.isEmpty {
                            Text("Loading…").tag("")
                        }
                        ForEach(enabledActions) { action in
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
        .frame(width: 400, height: 320)
        .task { await load() }
    }

    private var canSubmit: Bool {
        let hasWorkspace = !isCreatingWorkspace
            || !newWorkspaceName.trimmingCharacters(in: .whitespaces).isEmpty
        return !isSubmitting && selectedAction != nil && hasWorkspace && runtime != nil
    }

    private func load() async {
        guard let runtime else {
            loadError = "This project's connection is no longer configured."
            return
        }
        do {
            actions = try await runtime.client.actions()
            workspaces = try await runtime.client.workspaces(projectID: project.id, includeArchived: false)
            loadError = nil
            if selectedActionName.isEmpty {
                selectedActionName = actions.first(where: \.enabled)?.name ?? ""
            }
            if selectedWorkspaceID == Self.newWorkspaceTag, let first = workspaces.first {
                selectedWorkspaceID = first.id
            }
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func submit() async {
        guard let action = selectedAction, let runtime else { return }
        isSubmitting = true
        defer { isSubmitting = false }

        do {
            let workspaceID: String
            if isCreatingWorkspace {
                let created = try await runtime.client.createWorkspace(
                    projectID: project.id,
                    name: newWorkspaceName.trimmingCharacters(in: .whitespaces)
                )
                workspaceID = created.id
            } else {
                workspaceID = selectedWorkspaceID
            }

            let trimmedName = name.trimmingCharacters(in: .whitespaces)
            let request = StartSessionRequest(
                workspaceId: workspaceID,
                action: action.name,
                name: trimmedName.isEmpty ? nil : trimmedName
            )
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
