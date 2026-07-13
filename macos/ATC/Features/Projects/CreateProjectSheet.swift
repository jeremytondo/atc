import SwiftUI
import ATCAPI

/// Editable state behind the New Project sheet, split out so the
/// selection/clear rules are testable without hosting the view. The chosen
/// folder is Connection-specific, so switching Connections drops it.
@Observable
final class CreateProjectDraft {
    /// The Connection new projects are created on. Nil only before
    /// preselection or when no Connections exist.
    var connectionID: UUID?
    var name = ""
    var workingDir = ""

    init(connectionID: UUID? = nil) {
        self.connectionID = connectionID
    }

    /// Preselect the first Connection in creation order, once, without
    /// clobbering a choice the user already made.
    func preselectFirst(in runtimes: [ConnectionRuntime]) {
        guard connectionID == nil else { return }
        connectionID = runtimes.first?.id
    }

    /// Changing the selected Connection clears the chosen folder (browsing is
    /// server-specific) but keeps the typed name.
    func selectConnection(_ id: UUID?) {
        guard id != connectionID else { return }
        connectionID = id
        workingDir = ""
    }
}

/// Form for `POST /projects`: pick the owning Connection, a name, and a
/// workstation directory browsed through that Connection's folder picker.
struct CreateProjectSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    /// Called with the new project so the shell can expand/target it.
    var onCreated: (Project) -> Void = { _ in }

    @State private var draft = CreateProjectDraft()
    @State private var isSubmitting = false
    @State private var submitError: String?
    @State private var showFolderPicker = false

    /// The runtime new projects route through; nil only when no Connection is
    /// selected (i.e. there are none).
    private var selectedRuntime: ConnectionRuntime? {
        draft.connectionID.flatMap { appModel.runtime(id: $0) }
    }

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section {
                    Picker("Connection", selection: Binding(
                        get: { draft.connectionID },
                        set: { draft.selectConnection($0) }
                    )) {
                        // The selection is nil until onAppear preselects (and
                        // always when no Connections exist); keep a matching
                        // tag so AppKit doesn't log an invalid selection.
                        if selectedRuntime == nil {
                            Text(appModel.runtimes.isEmpty ? "No Connections" : "Select Connection")
                                .tag(draft.connectionID)
                        }
                        ForEach(appModel.runtimes) { runtime in
                            Text(runtime.record.name).tag(runtime.id as UUID?)
                        }
                    }
                    TextField("Name", text: $draft.name, prompt: Text("My Project"))
                    HStack {
                        TextField("Directory", text: $draft.workingDir, prompt: Text("/path/on/the/server"))
                            .autocorrectionDisabled()
                        Button {
                            showFolderPicker = true
                        } label: {
                            Image(systemName: "folder")
                        }
                        .disabled(selectedRuntime == nil)
                        .help("Browse folders on the server")
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
            .padding(Spacing.md)
        }
        .frame(width: 460, height: 290)
        .onAppear { draft.preselectFirst(in: appModel.runtimes) }
        .sheet(isPresented: $showFolderPicker) {
            if let runtime = selectedRuntime {
                RemoteFolderPickerSheet(client: runtime.client, initialPath: draft.workingDir) { path in
                    draft.workingDir = path
                    // Picking a folder before typing a name suggests one.
                    if draft.name.trimmingCharacters(in: .whitespaces).isEmpty {
                        draft.name = URL(filePath: path).lastPathComponent
                    }
                }
            }
        }
    }

    private var canSubmit: Bool {
        !isSubmitting
            && selectedRuntime != nil
            && !draft.name.trimmingCharacters(in: .whitespaces).isEmpty
            && !draft.workingDir.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private func submit() async {
        guard let runtime = selectedRuntime else { return }
        isSubmitting = true
        defer { isSubmitting = false }
        do {
            let project = try await runtime.projects.create(
                name: draft.name.trimmingCharacters(in: .whitespaces),
                workingDir: draft.workingDir.trimmingCharacters(in: .whitespaces)
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

#Preview("Two connections") {
    CreateProjectSheet()
        .environment(AppModel.preview(connections: [
            (name: "Workstation", client: MockATCClient()),
            (name: "Laptop", client: MockATCClient()),
        ]))
        .preferredColorScheme(.dark)
}
