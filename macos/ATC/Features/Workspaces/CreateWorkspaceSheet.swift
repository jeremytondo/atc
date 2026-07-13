import SwiftUI
import ATCAPI

/// Form for `POST /workspaces`: a name plus the owning Project, invoked
/// from three contexts (see `CreateWorkspaceContext.Mode`). Uses the
/// server's default Environment implicitly. On success, creation opens the
/// new Workspace — it does not merely select it.
struct CreateWorkspaceSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    let context: CreateWorkspaceContext
    /// Called with the new Workspace's ref so the window activates it.
    var onCreated: (WorkspaceRef) -> Void = { _ in }

    @State private var selectedProject: ProjectRef?
    @State private var name = ""
    @State private var isSubmitting = false
    @State private var submitError: String?

    /// Dashboard invocations fix the Project; the in-shell and File-menu
    /// forms keep the picker enabled.
    private var isProjectFixed: Bool {
        if case .fixed = context.mode { return true }
        return false
    }

    /// Unarchived Projects across every Connection, in Dashboard order.
    private var projectChoices: [(ref: ProjectRef, project: Project, connectionName: String)] {
        appModel.runtimes.flatMap { runtime in
            runtime.projects.projects
                .filter { !$0.isArchived }
                .map { (
                    ref: ProjectRef(connectionID: runtime.id, projectID: $0.id),
                    project: $0,
                    connectionName: runtime.record.name
                ) }
        }
    }

    private var showsConnectionNames: Bool {
        appModel.runtimes.count > 1
    }

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section {
                    if isProjectFixed {
                        LabeledContent("Project") {
                            Text(fixedProjectName)
                        }
                    } else {
                        Picker("Project", selection: $selectedProject) {
                            if selectedProject == nil {
                                Text("Select Project").tag(nil as ProjectRef?)
                            }
                            ForEach(projectChoices, id: \.ref) { choice in
                                Text(showsConnectionNames
                                    ? "\(choice.project.name) — \(choice.connectionName)"
                                    : choice.project.name)
                                    .tag(choice.ref as ProjectRef?)
                            }
                        }
                    }
                    TextField("Name", text: $name, prompt: Text("What are you working on?"))
                } footer: {
                    Text("The workspace uses its project's directory on the workstation.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }

                if let message = submitError ?? availabilityError {
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
                        Text("Create Workspace")
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(!canSubmit)
            }
            .padding(Spacing.md)
        }
        .frame(width: 420, height: 240)
        .onAppear {
            switch context.mode {
            case .fixed(let ref), .preselected(let ref):
                selectedProject = ref
            case .free:
                break
            }
        }
    }

    private var fixedProjectName: String {
        guard let ref = selectedProject,
              let project = appModel.runtime(id: ref.connectionID)?.projects.project(id: ref.projectID)
        else { return "" }
        return project.name
    }

    private var canSubmit: Bool {
        !isSubmitting
            && (selectedProject.map { appModel.canCreateWorkspace(in: $0) } ?? false)
            && !name.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private var availabilityError: String? {
        guard let selectedProject else { return nil }
        guard appModel.canCreateWorkspace(in: selectedProject) else {
            return "This project is archived or its connection is unavailable."
        }
        return nil
    }

    private func submit() async {
        guard let ref = selectedProject,
              appModel.canCreateWorkspace(in: ref),
              let runtime = appModel.runtime(id: ref.connectionID) else {
            submitError = "This project is archived or its connection is unavailable."
            return
        }
        isSubmitting = true
        defer { isSubmitting = false }
        do {
            let workspace = try await runtime.workspaces.create(
                projectID: ref.projectID,
                name: name.trimmingCharacters(in: .whitespaces)
            )
            submitError = nil
            dismiss()
            onCreated(WorkspaceRef(connectionID: ref.connectionID, workspaceID: workspace.id))
        } catch {
            submitError = error.localizedDescription
        }
    }
}

#Preview("Fixed project") {
    let appModel = AppModel.preview()
    CreateWorkspaceSheet(context: CreateWorkspaceContext(mode: .fixed(
        ProjectRef(connectionID: appModel.runtimes.first!.id, projectID: "prj_atelier")
    )))
    .environment(appModel)
    .preferredColorScheme(.dark)
}

#Preview("Context-free") {
    CreateWorkspaceSheet(context: CreateWorkspaceContext(mode: .free))
        .environment(AppModel.preview())
        .preferredColorScheme(.dark)
}
