import SwiftUI
import CockpitAPI

/// Form for `POST /projects`: a name plus a workstation directory picked
/// through the remote folder browser.
// Phase 5 adds a Connection selector; until then the first Connection
// (creation order) hosts new projects.
struct CreateProjectSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    /// Called with the new project so the shell can expand/target it.
    var onCreated: (Project) -> Void = { _ in }

    @State private var name = ""
    @State private var workingDir = ""
    @State private var isSubmitting = false
    @State private var submitError: String?
    @State private var showFolderPicker = false

    private var runtime: ConnectionRuntime? { appModel.runtimes.first }

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section {
                    TextField("Name", text: $name, prompt: Text("My Project"))
                    HStack {
                        TextField("Directory", text: $workingDir, prompt: Text("/path/on/the/server"))
                            .autocorrectionDisabled()
                        Button {
                            showFolderPicker = true
                        } label: {
                            Image(systemName: "folder")
                        }
                        .disabled(runtime == nil)
                        .help("Browse folders on the Cockpit workstation")
                    }
                } footer: {
                    Text("Sessions started in this project run in its directory on the workstation.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }

                if let message = submitError {
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
                        Text("Create Project")
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(!canSubmit)
            }
            .padding(12)
        }
        .frame(width: 460, height: 250)
        .sheet(isPresented: $showFolderPicker) {
            if let runtime {
                RemoteFolderPickerSheet(client: runtime.client, initialPath: workingDir) { path in
                    workingDir = path
                    // Picking a folder before typing a name suggests one.
                    if name.trimmingCharacters(in: .whitespaces).isEmpty {
                        name = URL(filePath: path).lastPathComponent
                    }
                }
            }
        }
    }

    private var canSubmit: Bool {
        !isSubmitting
            && runtime != nil
            && !name.trimmingCharacters(in: .whitespaces).isEmpty
            && !workingDir.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private func submit() async {
        guard let runtime else { return }
        isSubmitting = true
        defer { isSubmitting = false }
        do {
            let project = try await runtime.projects.create(
                name: name.trimmingCharacters(in: .whitespaces),
                workingDir: workingDir.trimmingCharacters(in: .whitespaces)
            )
            submitError = nil
            dismiss()
            onCreated(project)
        } catch {
            submitError = error.localizedDescription
        }
    }
}

#Preview {
    CreateProjectSheet()
        .environment(AppModel.preview())
        .preferredColorScheme(.dark)
}
