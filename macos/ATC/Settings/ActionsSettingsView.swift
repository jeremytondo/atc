import AppKit
import SwiftUI
import ATCAPI

/// What the editor pane is editing: an existing action by ID, or a new
/// draft that isn't on the server until Save.
enum ActionEditorTarget: Hashable {
    case existing(String)
    case new
}

/// Server-wide Action administration scoped to one Connection.
struct ActionsSettingsView: View {
    @Environment(AppModel.self) private var appModel

    @State private var connectionID: UUID?
    @State private var target: ActionEditorTarget?
    @State private var confirmRemove = false

    private var runtime: ConnectionRuntime? {
        connectionID.flatMap { appModel.runtime(id: $0) }
    }

    private var selectedAction: ATCAction? {
        guard case .existing(let id) = target else { return nil }
        return runtime?.actions.action(id: id)
    }

    var body: some View {
        if appModel.runtimes.isEmpty {
            ContentUnavailableView {
                Label("No Connections Configured", systemImage: "network.slash")
            } description: {
                Text("Actions live on an atc server. Add a connection in the Connections tab, then manage its actions here.")
            }
        } else {
            VStack(spacing: 0) {
                header
                Divider()
                HStack(spacing: 0) {
                    master
                        .frame(width: 240)
                    Divider()
                    detail
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
            .onAppear {
                if connectionID == nil {
                    connectionID = appModel.runtimes.first?.id
                }
            }
            .onChange(of: connectionID) { target = nil }
            .task(id: connectionID) { await runtime?.actions.refresh() }
            .confirmationDialog(removePrompt, isPresented: $confirmRemove) {
                Button("Delete Action", role: .destructive) {
                    remove()
                }
            } message: {
                Text("This removes the action from the atc server for every client. Existing sessions keep their copied launch identity.")
            }
        }
    }

    private var header: some View {
        HStack(spacing: Spacing.md) {
            Picker("Connection", selection: $connectionID) {
                if runtime == nil {
                    Text("Select Connection").tag(connectionID)
                }
                ForEach(appModel.runtimes) { runtime in
                    Text(runtime.record.name).tag(Optional(runtime.id))
                }
            }
            .fixedSize()
            if runtime?.actions.isLoading == true {
                ProgressView()
                    .controlSize(.small)
            }
            Spacer()
            if let error = runtime?.actions.lastError {
                Label(error, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(.red)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
        }
        .padding(.horizontal, Spacing.md)
        .padding(.vertical, Spacing.sm)
    }

    private var master: some View {
        VStack(spacing: 0) {
            List(selection: $target) {
                if let store = runtime?.actions {
                    ForEach(store.actions) { action in
                        HStack(spacing: Spacing.sm) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(action.name)
                                    .font(.headline)
                                    .lineLimit(1)
                                if let description = action.description {
                                    Text(description)
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                        .lineLimit(1)
                                }
                            }
                            .opacity(action.enabled ? 1 : Dimming.unavailable)
                            Spacer()
                            Toggle("Enabled", isOn: enabledBinding(for: action))
                                .toggleStyle(.switch)
                                .controlSize(.mini)
                                .labelsHidden()
                                .help(action.enabled
                                    ? "Disable — a disabled action is hidden when starting sessions"
                                    : "Enable this action")
                        }
                        .padding(.vertical, 2)
                        .tag(ActionEditorTarget.existing(action.id))
                        .contextMenu {
                            Button("Copy Action ID") {
                                copyToPasteboard(action.id)
                            }
                        }
                    }
                }
            }
            .overlay {
                if let store = runtime?.actions, store.hasLoadedOnce, store.actions.isEmpty {
                    ContentUnavailableView(
                        "No Actions",
                        systemImage: "bolt.slash",
                        description: Text("This server has no actions configured.")
                    )
                }
            }
            Divider()
            ListEditorBar(
                addHelp: "Add an action",
                removeHelp: "Delete the selected action",
                canRemove: selectedAction != nil,
                onAdd: { target = .new },
                onRemove: { confirmRemove = true }
            )
        }
    }

    @ViewBuilder
    private var detail: some View {
        if let runtime, let target {
            ActionEditorView(store: runtime.actions, target: target) { savedID in
                self.target = .existing(savedID)
            }
            .id(EditorIdentity(connectionID: runtime.id, target: target))
        } else {
            ContentUnavailableView(
                "No Action Selected",
                systemImage: "sidebar.right",
                description: Text("Choose an action to edit, or add a new one.")
            )
        }
    }

    private struct EditorIdentity: Hashable {
        let connectionID: UUID
        let target: ActionEditorTarget
    }

    private var removePrompt: String {
        "Delete “\(selectedAction?.name ?? "Action")”?"
    }

    private func enabledBinding(for action: ATCAction) -> Binding<Bool> {
        Binding(
            get: { runtime?.actions.action(id: action.id)?.enabled ?? action.enabled },
            set: { newValue in
                guard let store = runtime?.actions else { return }
                Task {
                    do {
                        try await store.setEnabled(id: action.id, enabled: newValue)
                    } catch {
                        store.lastError = error.localizedDescription
                    }
                }
            }
        )
    }

    private func remove() {
        guard let store = runtime?.actions, case .existing(let id) = target else { return }
        Task {
            do {
                try await store.delete(id: id)
                target = nil
            } catch {
                store.lastError = error.localizedDescription
            }
        }
    }
}

/// Editor for one complete Action definition. IDs stay out of the primary
/// form and are available only from the contextual info affordance.
private struct ActionEditorView: View {
    let store: ActionsStore
    let target: ActionEditorTarget
    var onSaved: (String) -> Void

    @State private var draft = ActionDraft()
    @State private var original: ATCAction?
    @State private var isSubmitting = false
    @State private var submitError: String?
    @State private var showsID = false

    private var isNew: Bool {
        if case .new = target { return true }
        return false
    }

    private var canSubmit: Bool {
        !isSubmitting && draft.validationMessage() == nil
    }

    var body: some View {
        VStack(spacing: 0) {
            form
            Divider()
            bottomBar
        }
        .onAppear { reseed() }
    }

    private var form: some View {
        Form {
            Section {
                LabeledContent("Name") {
                    HStack(spacing: Spacing.sm) {
                        TextField("Name", text: $draft.name, prompt: Text("Codex"))
                            .labelsHidden()
                        if original != nil {
                            Button {
                                showsID.toggle()
                            } label: {
                                Image(systemName: "info.circle")
                            }
                            .buttonStyle(.borderless)
                            .help("Show Action ID")
                            .popover(isPresented: $showsID) {
                                actionIDPopover
                            }
                        }
                    }
                }
                TextField("Description", text: $draft.descriptionText)
                Toggle("Agent action", isOn: $draft.isAgent)
                Toggle("Enabled", isOn: $draft.enabled)
            }

            Section {
                TextField("Command", text: $draft.command, prompt: Text("codex"))
                    .fontDesign(.monospaced)
                    .autocorrectionDisabled()
                LabeledContent("Arguments") {
                    TextEditor(text: $draft.argsText)
                        .font(.body.monospaced())
                        .frame(minHeight: 70, maxHeight: 140)
                        .scrollContentBackground(.hidden)
                        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: Radius.control))
                }
            } header: {
                Text("Command")
            } footer: {
                Text("One literal argument per line. Spaces are part of the argument.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
    }

    @ViewBuilder
    private var actionIDPopover: some View {
        if let original {
            VStack(alignment: .leading, spacing: Spacing.sm) {
                Text("Action ID")
                    .font(.headline)
                Text(original.id)
                    .font(.body.monospaced())
                    .textSelection(.enabled)
                Button("Copy") {
                    copyToPasteboard(original.id)
                }
            }
            .padding(Spacing.md)
        }
    }

    private var bottomBar: some View {
        HStack(spacing: Spacing.sm) {
            if let submitError {
                Label(submitError, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(.red)
                    .lineLimit(2)
            }
            Spacer()
            Button("Cancel") { reseed() }
                .disabled(isSubmitting)
            Button(isNew ? "Create" : "Save") { submit() }
                .keyboardShortcut(.defaultAction)
                .disabled(!canSubmit)
        }
        .padding(Spacing.md)
    }

    private func reseed() {
        switch target {
        case .new:
            original = nil
            draft = ActionDraft()
        case .existing(let id):
            original = store.action(id: id)
            draft = original.map(ActionDraft.init(action:)) ?? ActionDraft()
        }
        submitError = nil
    }

    private func submit() {
        submitError = nil
        if let message = draft.validationMessage() {
            submitError = message
            return
        }
        isSubmitting = true
        Task {
            defer { isSubmitting = false }
            do {
                let saved: ATCAction
                switch target {
                case .new:
                    saved = try await store.create(draft.createRequest())
                case .existing(let id):
                    saved = try await store.update(id: id, draft.patch())
                }
                original = saved
                draft = ActionDraft(action: saved)
                onSaved(saved.id)
            } catch {
                submitError = error.localizedDescription
            }
        }
    }
}

@MainActor
private func copyToPasteboard(_ value: String) {
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString(value, forType: .string)
}

#Preview("Actions — populated") {
    ActionsSettingsView()
        .environment(AppModel.preview())
        .frame(width: 760, height: 520)
        .preferredColorScheme(.dark)
}

#Preview("Actions — no connections") {
    ActionsSettingsView()
        .environment(AppModel(
            connections: ConnectionsStore(
                defaults: UserDefaults(suiteName: "preview.actions.empty")!,
                credentials: InMemoryCredentialStore()
            ),
            clientFactory: { _ in MockATCClient() }
        ))
        .frame(width: 760, height: 520)
        .preferredColorScheme(.dark)
}
