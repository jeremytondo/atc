import SwiftUI
import CockpitAPI

/// Form for `POST /sessions/start`, driven by live `/actions` and
/// `/environments` discovery.
struct CreateSessionSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    /// Called with the new session's ID so the shell can select it.
    var onCreated: (String) -> Void = { _ in }

    @State private var actions: [CockpitAction] = []
    @State private var environments: [CockpitEnvironment] = []
    @State private var loadError: String?

    @State private var selectedActionName = ""
    @State private var selectedEnvironmentName = ""
    @State private var workingDir = ""
    @State private var name = ""
    @State private var prompt = ""
    @State private var enumParams: [String: String] = [:]
    @State private var boolParams: [String: Bool] = [:]

    @State private var isSubmitting = false
    @State private var submitError: String?
    @State private var showFolderPicker = false

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
                    Picker("Environment", selection: $selectedEnvironmentName) {
                        ForEach(environments) { environment in
                            Text(environment.displayLabel).tag(environment.name)
                        }
                    }
                    HStack {
                        TextField("Working Directory", text: $workingDir, prompt: Text("/path/on/the/server"))
                            .autocorrectionDisabled()
                        Button {
                            showFolderPicker = true
                        } label: {
                            Image(systemName: "folder")
                        }
                        .help("Browse folders on the Cockpit workstation")
                    }
                    TextField("Name (optional)", text: $name)
                }

                if let action = selectedAction, action.acceptsPrompt {
                    Section("Prompt") {
                        TextEditor(text: $prompt)
                            .font(.body.monospaced())
                            .frame(minHeight: 70)
                    }
                }

                if let action = selectedAction, !action.params.isEmpty {
                    Section("Parameters") {
                        ForEach(action.params.keys.sorted(), id: \.self) { key in
                            if let spec = action.params[key] {
                                paramField(key: key, spec: spec)
                            }
                        }
                    }
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
                        Text("Create Session")
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(!canSubmit)
            }
            .padding(12)
        }
        .frame(width: 460, height: 440)
        .task { await loadDiscovery() }
        .onChange(of: selectedActionName) { resetParams() }
        .sheet(isPresented: $showFolderPicker) {
            RemoteFolderPickerSheet(client: appModel.client, initialPath: workingDir) { path in
                workingDir = path
            }
        }
    }

    @ViewBuilder
    private func paramField(key: String, spec: CockpitAction.ParamSpec) -> some View {
        let label = spec.label ?? key
        if spec.isEnum, let values = spec.values {
            Picker(label, selection: Binding(
                get: { enumParams[key] ?? "" },
                set: { enumParams[key] = $0 }
            )) {
                ForEach(values, id: \.self) { value in
                    Text(value).tag(value)
                }
            }
        } else if spec.isBool {
            Toggle(label, isOn: Binding(
                get: { boolParams[key] ?? false },
                set: { boolParams[key] = $0 }
            ))
        } else {
            LabeledContent(label, value: "unsupported type \(spec.type)")
                .foregroundStyle(.secondary)
        }
    }

    private var canSubmit: Bool {
        !isSubmitting
            && selectedAction != nil
            && !workingDir.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private func loadDiscovery() async {
        do {
            async let actionsTask = appModel.client.actions()
            async let environmentsTask = appModel.client.environments()
            (actions, environments) = try await (actionsTask, environmentsTask)
            loadError = nil
            if selectedActionName.isEmpty {
                selectedActionName = actions.first(where: \.enabled)?.name ?? ""
            }
            if selectedEnvironmentName.isEmpty {
                selectedEnvironmentName = (environments.first(where: \.isDefault) ?? environments.first)?.name ?? ""
            }
            resetParams()
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func resetParams() {
        enumParams = [:]
        boolParams = [:]
        guard let action = selectedAction else { return }
        for (key, spec) in action.params {
            if spec.isEnum {
                if case .string(let value)? = spec.default {
                    enumParams[key] = value
                } else {
                    enumParams[key] = spec.values?.first ?? ""
                }
            } else if spec.isBool {
                if case .bool(let value)? = spec.default {
                    boolParams[key] = value
                } else {
                    boolParams[key] = false
                }
            }
        }
    }

    private func submit() async {
        guard let action = selectedAction else { return }
        isSubmitting = true
        defer { isSubmitting = false }

        var params: [String: JSONValue] = [:]
        for (key, spec) in action.params {
            if spec.isEnum, let value = enumParams[key], !value.isEmpty {
                params[key] = .string(value)
            } else if spec.isBool, let value = boolParams[key] {
                params[key] = .bool(value)
            }
        }

        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        let trimmedPrompt = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        let request = StartSessionRequest(
            action: action.name,
            environment: selectedEnvironmentName.isEmpty ? nil : selectedEnvironmentName,
            params: params.isEmpty ? nil : params,
            workingDir: workingDir.trimmingCharacters(in: .whitespaces),
            prompt: (action.acceptsPrompt && !trimmedPrompt.isEmpty) ? trimmedPrompt : nil,
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
    CreateSessionSheet()
        .environment(AppModel(client: MockCockpitClient()))
        .preferredColorScheme(.dark)
}
