// Form for `POST /projects`: which Connection owns it, a name, and an
// existing directory on that server. ATC never creates the directory, so the
// server's `checkDirectory` verdict is what gates Create.

import ATCAppServerAPI
import SwiftUI

struct CreateProjectSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    /// Called with the new project so a caller can target it.
    var onCreated: (Project) -> Void = { _ in }

    @State private var connectionID: UUID?
    @State private var name = ""
    @State private var workingDirectory = ""
    @State private var directoryState: DirectoryCheckState = .idle
    @State private var isSubmitting = false
    @State private var submitError: String?

    var body: some View {
        SheetScaffold(
            title: "New Project",
            systemImage: "folder.badge.plus",
            primaryLabel: "Create Project",
            isBusy: isSubmitting,
            canSubmit: canSubmit,
            onCancel: { dismiss() },
            onSubmit: { Task { await submit() } }
        ) {
            Section {
                Picker("Connection", selection: Binding(
                    get: { connectionID },
                    set: { connectionID = $0 }
                )) {
                    // The selection is nil until `.task` preselects (and
                    // always when no Connections exist); keep a matching tag
                    // so AppKit doesn't log an invalid selection.
                    if selectedRuntime == nil {
                        Text(appModel.runtimes.isEmpty ? "No Connections" : "Select Connection")
                            .tag(UUID?.none)
                    }
                    ForEach(appModel.runtimes) { runtime in
                        Text(runtime.record.name).tag(UUID?.some(runtime.id))
                    }
                }
                TextField("Name", text: $name, prompt: Text("My Project"))
            }

            Section {
                WorkingDirectoryField(
                    label: "Default Working Directory",
                    path: $workingDirectory,
                    client: selectedRuntime?.client,
                    connectionID: selectedRuntime?.id,
                    state: $directoryState
                )
            } footer: {
                Text("New threads and terminals in this project start here.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            if let submitError {
                Section {
                    Label(submitError, systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.red)
                        .font(.callout)
                }
            }
        }
        .frame(width: 480, height: 340)
        .task {
            if connectionID == nil {
                connectionID = appModel.runtimes.first?.id
            }
        }
    }

    private var selectedRuntime: ConnectionRuntime? {
        connectionID.flatMap { appModel.runtime(id: $0) }
    }

    private var canSubmit: Bool {
        !isSubmitting
            && selectedRuntime != nil
            && !name.trimmingCharacters(in: .whitespaces).isEmpty
            && directoryState.isAvailable
    }

    private func submit() async {
        guard let runtime = selectedRuntime else { return }
        isSubmitting = true
        defer { isSubmitting = false }
        do {
            let project = try await runtime.projects.create(
                name: name.trimmingCharacters(in: .whitespaces),
                defaultWorkingDirectory: workingDirectory.trimmingCharacters(in: .whitespaces)
            )
            submitError = nil
            dismiss()
            onCreated(project)
        } catch {
            submitError = error.localizedDescription
        }
    }
}
