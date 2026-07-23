import SwiftUI
import ATCAPI

enum WorkspaceStartupEditorTarget: Hashable, Identifiable {
    case connection(UUID)
    case project(ProjectRef)

    var id: Self { self }
}

enum WorkspaceStartupSummary {
    static func text(
        configuration: StartupConfiguration,
        actions: [ATCAction],
        hasLoadedOnce: Bool
    ) -> String {
        guard !configuration.entries.isEmpty,
              let defaultID = configuration.defaultEntryID,
              let defaultEntry = configuration.entries.first(where: { $0.id == defaultID })
        else { return "None configured" }

        let count = configuration.entries.count
        let countText = count == 1 ? "1 Session" : "\(count) Sessions"
        let name = defaultName(
            for: defaultEntry, actions: actions, hasLoadedOnce: hasLoadedOnce
        )
        return "\(countText) · Default: \(name)"
    }

    private static func defaultName(
        for entry: StartupEntry,
        actions: [ATCAction],
        hasLoadedOnce: Bool
    ) -> String {
        switch entry.target {
        case .shell:
            return "Shell"
        case .action(let id):
            let action = actions.first { $0.id == id }
            // Mirrors the editor's identity labels: before the registry has
            // loaded, an unknown Action is unvalidated rather than missing.
            guard hasLoadedOnce else { return action?.name ?? "Action" }
            guard let action, action.enabled else { return "Unavailable Action" }
            return action.name
        }
    }
}

/// Shared sheet shell used by Connection Settings and both Project menus.
struct WorkspaceStartupEditorSheet: View {
    @Environment(\.dismiss) private var dismiss
    let target: WorkspaceStartupEditorTarget

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: Spacing.sm) {
                Image(systemName: "rectangle.stack.badge.plus")
                    .foregroundStyle(.secondary)
                Text("Workspace Startup")
                    .font(.title2.weight(.semibold))
                Spacer()
            }
            .padding(Spacing.lg)

            Divider()
            WorkspaceStartupEditor(target: target)
            Divider()

            HStack {
                Spacer()
                Button("Done") { dismiss() }
                    .keyboardShortcut(.defaultAction)
            }
            .padding(Spacing.md)
        }
        .frame(width: 580, height: 520)
    }
}

/// Reusable editor for either Connection defaults or one Project override.
/// All mutations write through immediately to the local preference store.
struct WorkspaceStartupEditor: View {
    @Environment(AppModel.self) private var appModel
    let target: WorkspaceStartupEditorTarget

    private var store: WorkspaceStartupStore { appModel.workspaceStartup }

    private var connectionID: UUID {
        switch target {
        case .connection(let id): id
        case .project(let ref): ref.connectionID
        }
    }

    private var projectRef: ProjectRef? {
        guard case .project(let ref) = target else { return nil }
        return ref
    }

    private var runtime: ConnectionRuntime? {
        appModel.runtime(id: connectionID)
    }

    private var actions: [ATCAction] {
        runtime?.actions.actions ?? []
    }

    private var configuration: StartupConfiguration {
        switch target {
        case .connection(let connectionID):
            return store.connectionConfiguration(connectionID: connectionID)
        case .project(let ref):
            return store.resolvedConfiguration(
                connectionID: ref.connectionID,
                projectID: ref.projectID
            )
        }
    }

    private var projectMode: ProjectStartupMode? {
        projectRef.map {
            store.projectMode(connectionID: $0.connectionID, projectID: $0.projectID)
        }
    }

    private var isDirectlyEditable: Bool {
        switch target {
        case .connection:
            return true
        case .project:
            return projectMode == .custom
        }
    }

    private var isReachable: Bool {
        guard let runtime else { return false }
        if case .connected = runtime.reachability { return true }
        return false
    }

    private var validation: StartupConfigurationValidation {
        StartupEntryValidator.validate(
            configuration: configuration,
            actions: actions,
            hasLoadedOnce: runtime?.actions.hasLoadedOnce ?? false,
            isReachable: isReachable
        )
    }

    private var canEditEntries: Bool {
        isDirectlyEditable && validation.canEdit
    }

    var body: some View {
        Form {
            if let ref = projectRef {
                Section("Project Configuration") {
                    Picker("Mode", selection: projectModeBinding(for: ref)) {
                        Text("Use Connection Defaults")
                            .tag(ProjectStartupMode.useConnectionDefaults)
                        Text("Custom")
                            .tag(ProjectStartupMode.custom)
                    }
                    .pickerStyle(.segmented)

                    if projectMode == .useConnectionDefaults {
                        Text("This Project follows the Connection configuration, including future changes.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }

            Section {
                if configuration.entries.isEmpty {
                    ContentUnavailableView {
                        Label("No Startup Sessions", systemImage: "rectangle.stack")
                    } description: {
                        Text("Add Actions or an Interactive Shell to use when a Workspace is created.")
                    }
                    .frame(maxWidth: .infinity, minHeight: 150)
                } else {
                    ForEach(configuration.entries) { entry in
                        entryRow(entry)
                    }
                }
            } header: {
                HStack {
                    Text(projectMode == .useConnectionDefaults ? "Inherited Sessions" : "Sessions")
                    Spacer()
                    addMenu
                }
            } footer: {
                if projectMode == .useConnectionDefaults {
                    Text("Switch to Custom to edit these sessions.")
                } else if !validation.canEdit {
                    Text("The Action registry is unavailable. Saved entries are read-only until this Connection loads successfully; you can still clear the configuration.")
                } else {
                    Text("Startup order after the Default Session is not significant.")
                }
            }

            if !configuration.entries.isEmpty, isDirectlyEditable {
                Section {
                    Button("Clear Configuration", role: .destructive) {
                        updateConfiguration { $0.removeAll() }
                    }
                }
            }
        }
        .formStyle(.grouped)
    }

    private var addMenu: some View {
        let enabledActions = actions.filter(\.enabled)
        return Menu {
            Button("Interactive Shell", systemImage: "terminal") {
                updateConfiguration { $0.add(target: .shell) }
            }
            if !enabledActions.isEmpty {
                Divider()
                ForEach(enabledActions) { action in
                    Button(action.name) {
                        updateConfiguration { $0.add(target: .action(id: action.id)) }
                    }
                }
            }
        } label: {
            Label("Add", systemImage: "plus")
        }
        .disabled(!canEditEntries)
        .help(addHelp)
    }

    private var addHelp: String {
        if projectMode == .useConnectionDefaults {
            return "Switch to Custom to add startup sessions"
        }
        return canEditEntries
            ? "Add a startup session"
            : "A loaded Action registry is required"
    }

    @ViewBuilder
    private func entryRow(_ entry: StartupEntry) -> some View {
        let validated = validation.entry(id: entry.id)
        VStack(alignment: .leading, spacing: Spacing.sm) {
            HStack(spacing: Spacing.sm) {
                Label(
                    identityName(for: entry, validated: validated),
                    systemImage: identityImage(for: entry)
                )
                .font(.headline)

                if configuration.defaultEntryID == entry.id {
                    Label("Default", systemImage: "star.fill")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Color.accentColor)
                } else {
                    Button("Make Default", systemImage: "star") {
                        updateConfiguration { $0.transferDefault(to: entry.id) }
                    }
                    .labelStyle(.iconOnly)
                    .buttonStyle(.borderless)
                    .disabled(!canEditEntries)
                    .help("Make Default Session")
                }

                Spacer()

                Button("Remove", systemImage: "minus.circle", role: .destructive) {
                    updateConfiguration { $0.remove(id: entry.id) }
                }
                .labelStyle(.iconOnly)
                .buttonStyle(.borderless)
                .disabled(!canEditEntries)
            }

            TextField(
                "Custom Name (optional)",
                text: Binding(
                    get: { entry.customName ?? "" },
                    set: { name in
                        updateConfiguration { $0.setCustomName(name, for: entry.id) }
                    }
                )
            )
            .disabled(!canEditEntries)

            if let diagnostic = diagnosticText(for: entry, validated: validated) {
                Text(diagnostic)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
        }
        .padding(.vertical, Spacing.xs)
        .help(helpText(for: entry, validated: validated))
    }

    private func projectModeBinding(for ref: ProjectRef) -> Binding<ProjectStartupMode> {
        Binding(
            get: {
                store.projectMode(connectionID: ref.connectionID, projectID: ref.projectID)
            },
            set: { mode in
                store.setProjectMode(
                    mode,
                    connectionID: ref.connectionID,
                    projectID: ref.projectID
                )
            }
        )
    }

    private func updateConfiguration(_ update: (inout StartupConfiguration) -> Void) {
        switch target {
        case .connection(let connectionID):
            store.updateConnectionConfiguration(connectionID: connectionID, update)
        case .project(let ref):
            store.updateProjectConfiguration(
                connectionID: ref.connectionID,
                projectID: ref.projectID,
                update
            )
        }
    }

    private func identityName(
        for entry: StartupEntry,
        validated: ValidatedStartupEntry?
    ) -> String {
        switch entry.target {
        case .shell:
            return "Shell"
        case .action:
            switch validated?.availability {
            case .disabled, .missing:
                return "Unavailable Action"
            case .valid, .unableToValidate:
                return validated?.cachedActionName ?? "Action"
            case nil:
                return "Action"
            }
        }
    }

    private func identityImage(for entry: StartupEntry) -> String {
        switch entry.target {
        case .shell: "terminal"
        case .action: "play.circle"
        }
    }

    private func diagnosticText(
        for entry: StartupEntry,
        validated: ValidatedStartupEntry?
    ) -> String? {
        guard case .action(let id) = entry.target else { return nil }
        switch validated?.availability {
        case .disabled:
            return "Disabled · \(id)"
        case .missing:
            return "Missing · \(id)"
        case .unableToValidate where validated?.cachedActionName == nil:
            return "Unable to validate · \(id)"
        case .valid, .unableToValidate, nil:
            return nil
        }
    }

    private func helpText(
        for entry: StartupEntry,
        validated: ValidatedStartupEntry?
    ) -> String {
        switch entry.target {
        case .shell:
            return validated?.availability == .unableToValidate
                ? "Unable to validate Interactive Shell while the Connection is unavailable"
                : "Interactive Shell"
        case .action(let id):
            return "Action ID: \(id)"
        }
    }
}
