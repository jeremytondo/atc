// Form for `POST /terminals` in one Project. Creation starts the zmx session
// immediately, so the sheet only closes once the server has answered — the
// new terminal is selected from the record it returns.

import ATCAppServerAPI
import SwiftUI

struct NewTerminalSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    let projectRef: ProjectRef

    @State private var name = ""
    @State private var workingDirectory = ""
    @State private var directoryState: DirectoryCheckState = .idle
    @State private var isSubmitting = false
    @State private var submitError: String?

    var body: some View {
        SheetScaffold(
            title: "New Terminal",
            systemImage: "terminal",
            primaryLabel: "Create Terminal",
            isBusy: isSubmitting,
            canSubmit: !isSubmitting && directoryState.isAvailable,
            onCancel: { windowState.newTerminalProject = nil },
            onSubmit: { Task { await submit() } }
        ) {
            Section {
                LabeledContent("Project", value: project?.name ?? "Unknown Project")
                TextField("Name (optional)", text: $name)
            }

            Section {
                WorkingDirectoryField(
                    label: "Working Directory",
                    path: $workingDirectory,
                    client: runtime?.client,
                    connectionID: runtime?.id,
                    state: $directoryState
                )
            } footer: {
                Text("The terminal starts an interactive login shell in this directory.")
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
        .frame(width: 480, height: 320)
        .task {
            if workingDirectory.isEmpty {
                workingDirectory = project?.defaultWorkingDirectory ?? ""
            }
        }
    }

    private var runtime: ConnectionRuntime? {
        appModel.runtime(id: projectRef.connectionID)
    }

    private var project: Project? {
        runtime?.projects.project(id: projectRef.projectID)
    }

    private func submit() async {
        guard let runtime else { return }
        isSubmitting = true
        defer { isSubmitting = false }
        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        do {
            let terminal = try await runtime.terminals.create(.init(
                projectId: projectRef.projectID,
                name: trimmedName.isEmpty ? nil : trimmedName,
                workingDirectory: workingDirectory.trimmingCharacters(in: .whitespaces)
            ))
            submitError = nil
            windowState.newTerminalProject = nil
            windowState.selectTerminal(
                TerminalRef(connectionID: projectRef.connectionID, terminalID: terminal.id),
                in: appModel
            )
        } catch {
            submitError = error.localizedDescription
        }
    }
}
