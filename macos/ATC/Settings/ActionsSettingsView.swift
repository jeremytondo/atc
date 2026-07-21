import SwiftUI
import ATCAPI

/// What the editor pane is editing: an existing action by name, or a new
/// draft that isn't on the server until Save.
enum ActionEditorTarget: Hashable {
    case existing(String)
    case new
}

/// The Actions settings section. Actions live on the atc server, so the
/// section is scoped to one Connection at a time via the picker up top;
/// below it, the same master list + draft editor shape as Connections.
///
/// Origin drives the affordances: built-ins can be edited (creating an
/// override) but not deleted; modified built-ins offer Revert to Default;
/// custom actions delete outright.
struct ActionsSettingsView: View {
    @Environment(AppModel.self) private var appModel

    @State private var connectionID: UUID?
    @State private var target: ActionEditorTarget?
    @State private var confirmRemove = false
    /// Bumped when a revert rewrites the selected action server-side, so the
    /// editor reloads even though the target name didn't change.
    @State private var editorGeneration = 0

    private var runtime: ConnectionRuntime? {
        connectionID.flatMap { appModel.runtime(id: $0) }
    }

    private var selectedAction: ATCAction? {
        if case .existing(let name) = target {
            return runtime?.actions.action(name: name)
        }
        return nil
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
                Button(
                    selectedAction?.isModified == true ? "Revert to Default" : "Delete Action",
                    role: .destructive
                ) {
                    remove()
                }
            } message: {
                Text(
                    selectedAction?.isModified == true
                        ? "Your customizations are discarded and the built-in definition comes back."
                        : "This removes the action from the atc server for every client."
                )
            }
        }
    }

    // MARK: Header

    private var header: some View {
        HStack(spacing: Spacing.md) {
            Picker("Connection", selection: $connectionID) {
                // The selection is nil until onAppear picks the first runtime
                // (and dangles if that Connection is deleted); keep a matching
                // tag so AppKit doesn't log an invalid selection.
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

    // MARK: Master list

    private var master: some View {
        VStack(spacing: 0) {
            List(selection: $target) {
                if let store = runtime?.actions {
                    ForEach(store.actions) { action in
                        HStack(spacing: Spacing.sm) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(action.displayLabel)
                                    .font(.headline)
                                    .lineLimit(1)
                                HStack(spacing: Spacing.sm) {
                                    Text(action.name)
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                        .lineLimit(1)
                                    TagBadge(text: Self.originLabel(action.origin))
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
                        .tag(ActionEditorTarget.existing(action.name))
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
                removeHelp: minusHelp,
                canRemove: selectedAction != nil && selectedAction?.isBuiltin != true,
                onAdd: { target = .new },
                onRemove: { confirmRemove = true }
            )
        }
    }

    // MARK: Detail

    @ViewBuilder
    private var detail: some View {
        if let runtime, let target {
            ActionEditorView(store: runtime.actions, target: target) { savedName in
                self.target = .existing(savedName)
            }
            // Reseed cleanly when the connection, target, or server-side
            // content (revert) changes.
            .id(EditorIdentity(connectionID: runtime.id, target: target, generation: editorGeneration))
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
        let generation: Int
    }

    /// Where an action's definition comes from, for its list badge.
    private static func originLabel(_ origin: String) -> String {
        switch origin {
        case "builtin": "Built-in"
        case "modified": "Modified"
        default: "Custom"
        }
    }

    // MARK: Mutations

    private var removePrompt: String {
        let label = selectedAction?.displayLabel ?? "Action"
        return selectedAction?.isModified == true
            ? "Revert “\(label)” to its built-in definition?"
            : "Delete “\(label)”?"
    }

    private var minusHelp: String {
        switch selectedAction?.origin {
        case "builtin": "Built-in actions can't be removed"
        case "modified": "Revert the selected action to its built-in definition"
        default: "Delete the selected action"
        }
    }

    private func enabledBinding(for action: ATCAction) -> Binding<Bool> {
        Binding(
            get: { runtime?.actions.action(name: action.name)?.enabled ?? action.enabled },
            set: { newValue in
                guard let store = runtime?.actions else { return }
                Task {
                    do {
                        try await store.setEnabled(name: action.name, enabled: newValue)
                    } catch {
                        store.lastError = error.localizedDescription
                    }
                }
            }
        )
    }

    private func remove() {
        guard let store = runtime?.actions, case .existing(let name) = target else { return }
        let wasModified = store.action(name: name)?.isModified == true
        Task {
            do {
                try await store.delete(name: name)
                if wasModified {
                    // The action still exists (reverted); reload its editor.
                    editorGeneration += 1
                } else {
                    target = nil
                }
            } catch {
                store.lastError = error.localizedDescription
            }
        }
    }
}

/// Draft editor for one action. Existing actions load their full definition
/// first (the list omits `command`/`args`); nothing reaches the server until
/// Save, which sends a full replace. Recreated per target via `.id(...)`.
private struct ActionEditorView: View {
    let store: ActionsStore
    let target: ActionEditorTarget
    /// Called after a successful save with the action's name so the parent
    /// can keep it selected (a new draft becomes an existing selection).
    var onSaved: (String) -> Void

    @State private var draft = ActionDraft()
    /// The loaded server definition for existing targets; drives the origin
    /// footnote and Cancel's reseed.
    @State private var original: ATCAction?
    @State private var isLoading = false
    @State private var loadError: String?
    @State private var isSubmitting = false
    @State private var submitError: String?

    private var isNew: Bool {
        if case .new = target { return true }
        return false
    }

    private var canSubmit: Bool {
        !isSubmitting && !draft.command.trimmingCharacters(in: .whitespaces).isEmpty
    }

    var body: some View {
        VStack(spacing: 0) {
            if isLoading {
                Spacer()
                ProgressView()
                Spacer()
            } else if let loadError {
                ContentUnavailableView {
                    Label("Couldn’t Load Action", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(loadError)
                } actions: {
                    Button("Retry") {
                        Task { await load() }
                    }
                }
            } else {
                form
                Divider()
                bottomBar
            }
        }
        .task { await load() }
    }

    private var form: some View {
        Form {
            Section {
                TextField("Label", text: $draft.label)
                if isNew {
                    TextField(
                        "Name",
                        text: $draft.name,
                        prompt: Text(draft.derivedName.isEmpty ? "derived from label" : draft.derivedName)
                    )
                    .autocorrectionDisabled()
                } else {
                    LabeledContent("Name", value: draft.name)
                }
                TextField("Description", text: $draft.descriptionText)
                Toggle("Enabled", isOn: $draft.enabled)
            } footer: {
                if original?.isBuiltin == true {
                    Text("Built-in action — saving creates an override you can revert later.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else if original?.isModified == true {
                    Text("Overrides a built-in action. Use − in the list to revert to the default.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }

            Section {
                TextField("Command", text: $draft.command, prompt: Text("lazygit"))
                    .fontDesign(.monospaced)
                    .autocorrectionDisabled()
                LabeledContent("Arguments") {
                    TextEditor(text: $draft.argsText)
                        .font(.body.monospaced())
                        .frame(minHeight: 44, maxHeight: 88)
                        .scrollContentBackground(.hidden)
                        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: Radius.control))
                }
                .help("One argument per line")
            } header: {
                Text("Command")
            } footer: {
                Text("One argument per line.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section {
                Toggle("Accepts an initial prompt", isOn: $draft.acceptsPrompt)
                if draft.acceptsPrompt {
                    TextField("Prompt flag", text: $draft.promptFlag, prompt: Text("--prompt"))
                        .fontDesign(.monospaced)
                        .autocorrectionDisabled()
                }
            } header: {
                Text("Prompt")
            } footer: {
                if draft.acceptsPrompt {
                    Text("Leave the flag empty to pass the prompt as a positional argument.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }

            Section {
                ForEach(draft.params) { param in
                    ActionParamEditor(param: param) {
                        draft.params.removeAll { $0.id == param.id }
                    }
                }
                Button {
                    draft.params.append(ParamDraft())
                } label: {
                    Label("Add Parameter", systemImage: "plus")
                }
                .buttonStyle(.borderless)
            } header: {
                Text("Parameters")
            } footer: {
                Text("Typed launch options shown when starting a session: a choice list or an on/off switch.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
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

    // MARK: Actions

    private func load() async {
        guard case .existing(let name) = target else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            let detail = try await store.detail(name: name)
            original = detail
            draft = ActionDraft(action: detail)
            loadError = nil
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func reseed() {
        draft = original.map(ActionDraft.init(action:)) ?? ActionDraft()
        submitError = nil
    }

    private func submit() {
        submitError = nil
        if let message = draft.validationMessage(isNew: isNew) {
            submitError = message
            return
        }
        isSubmitting = true
        Task {
            defer { isSubmitting = false }
            do {
                switch target {
                case .new:
                    let created = try await store.create(draft.writeRequest(routeName: nil))
                    onSaved(created.name)
                case .existing(let name):
                    let updated = try await store.update(name: name, draft.writeRequest(routeName: name))
                    original = updated
                    draft = ActionDraft(action: updated)
                    onSaved(updated.name)
                }
            } catch {
                submitError = error.localizedDescription
            }
        }
    }
}

/// Editor for one entry of the params map. A small boxed sub-form: name and
/// type up top, then the type-specific fields.
private struct ActionParamEditor: View {
    @Bindable var param: ParamDraft
    var onRemove: () -> Void

    var body: some View {
        GroupBox {
            VStack(alignment: .leading, spacing: Spacing.sm) {
                HStack(spacing: Spacing.sm) {
                    TextField("Name", text: $param.name, prompt: Text("model"))
                        .fontDesign(.monospaced)
                        .autocorrectionDisabled()
                    Picker("Type", selection: $param.isEnum) {
                        Text("Choices").tag(true)
                        Text("Switch").tag(false)
                    }
                    .labelsHidden()
                    .fixedSize()
                    Button {
                        onRemove()
                    } label: {
                        Image(systemName: "trash")
                    }
                    .buttonStyle(.borderless)
                    .help("Remove this parameter")
                }
                if param.isEnum {
                    TextField("Values", text: $param.valuesText, prompt: Text("fast, smart"))
                        .fontDesign(.monospaced)
                        .autocorrectionDisabled()
                    Picker("Default", selection: $param.defaultValue) {
                        Text("None").tag("")
                        ForEach(param.uniqueValues, id: \.self) { value in
                            Text(value).tag(value)
                        }
                        // Keep a stale default selectable so the picker never
                        // holds a missing tag; validation flags it on save.
                        if !param.defaultValue.isEmpty && !param.uniqueValues.contains(param.defaultValue) {
                            Text(param.defaultValue).tag(param.defaultValue)
                        }
                    }
                } else {
                    Toggle("Default on", isOn: $param.boolDefault)
                }
                TextField("Flag", text: $param.flag, prompt: Text("--model"))
                    .fontDesign(.monospaced)
                    .autocorrectionDisabled()
                TextField("Label", text: $param.label, prompt: Text("Model"))
            }
            .padding(Spacing.xs)
        }
    }
}

#Preview("Actions — populated") {
    ActionsSettingsView()
        .environment(AppModel.preview())
        .frame(width: 760, height: 520)
        .preferredColorScheme(.dark)
}

#Preview("Editor — codex (params)") {
    ActionEditorView(
        store: ActionsStore(client: MockATCClient()),
        target: .existing("codex"),
        onSaved: { _ in }
    )
    .frame(width: 500, height: 540)
    .preferredColorScheme(.dark)
}

#Preview("Actions — no connections") {
    ActionsSettingsView()
        .environment(AppModel(
            connections: ConnectionsStore(defaults: UserDefaults(suiteName: "preview.actions.empty")!, credentials: InMemoryCredentialStore()),
            clientFactory: { _ in MockATCClient() }
        ))
        .frame(width: 760, height: 520)
        .preferredColorScheme(.dark)
}
