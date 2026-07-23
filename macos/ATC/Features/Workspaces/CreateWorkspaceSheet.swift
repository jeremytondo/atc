import SwiftUI
import ATCAPI

/// Form for `POST /workspaces`: a name plus the owning Project, invoked
/// from three contexts (see `CreateWorkspaceContext.Mode`). On success,
/// creation opens the new Workspace — it does not merely select it.
struct CreateWorkspaceSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    let context: CreateWorkspaceContext
    /// Called after dismissal so the window activates the Workspace and,
    /// for configured startup, selects the real Default Session.
    var onCreated: (WorkspaceRef, SessionRef?) -> Void = { _, _ in }
    var onNotice: (StartupNotice) -> Void = { _ in }
    var onEditStartupSettings: (ProjectRef) -> Void = { _ in }

    @State private var selectedProject: ProjectRef?
    @State private var name = ""
    @State private var isSubmitting = false
    @State private var submitError: String?
    @State private var coordinator: WorkspaceStartupCoordinator?

    /// Dashboard invocations fix the Project; the in-shell and File-menu
    /// forms keep the picker enabled.
    private var isProjectFixed: Bool {
        if case .fixed = context.mode { return true }
        return false
    }

    /// Projects across every Connection, in Dashboard order.
    private var projectChoices: [(ref: ProjectRef, project: Project, connectionName: String)] {
        appModel.runtimes.flatMap { runtime in
            runtime.projects.projects.map { (
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
        SheetScaffold(
            title: "New Workspace",
            systemImage: "square.on.square",
            primaryLabel: primaryLabel,
            isBusy: isBusy,
            canSubmit: canSubmit,
            cancelDisabled: isDismissDisabled,
            primaryIndicator: primaryIndicator,
            secondaryLabel: secondaryLabel,
            onSecondary: secondaryAction,
            onCancel: { dismiss() },
            onSubmit: { handlePrimaryAction() }
        ) {
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
            .disabled(inputsDisabled)

            if cue == .defaultUnavailable {
                Section {
                    Label(
                        "Configured Default Session is unavailable.",
                        systemImage: "exclamationmark.triangle.fill"
                    )
                    .foregroundStyle(.orange)
                    .font(.callout)
                    Button("Edit Startup Settings") {
                        // The handler dismisses this sheet by clearing its
                        // presentation state, then presents the editor.
                        guard let selectedProject else { return }
                        onEditStartupSettings(selectedProject)
                    }
                }
            }

            if let progressLabel = coordinator?.progressLabel {
                Section {
                    Label {
                        Text(progressLabel)
                    } icon: {
                        ProgressView().controlSize(.small)
                    }
                    .font(.callout)
                }
            }

            if let message = submitError ?? coordinator?.errorMessage ?? availabilityError {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.red)
                        .font(.callout)
                }
            }
        }
        .frame(width: 460, height: resolvedConfiguration.entries.isEmpty ? 280 : 340)
        .interactiveDismissDisabled(isDismissDisabled)
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

    private var resolvedConfiguration: StartupConfiguration {
        guard let selectedProject else { return .empty }
        return appModel.workspaceStartup.resolvedConfiguration(
            connectionID: selectedProject.connectionID,
            projectID: selectedProject.projectID
        )
    }

    private var validation: StartupConfigurationValidation {
        guard let selectedProject,
              let runtime = appModel.runtime(id: selectedProject.connectionID)
        else {
            return StartupConfigurationValidation(entries: [], canEdit: false)
        }
        let isReachable: Bool
        if case .connected = runtime.reachability {
            isReachable = true
        } else {
            isReachable = false
        }
        return StartupEntryValidator.validate(
            configuration: resolvedConfiguration,
            actions: runtime.actions.actions,
            hasLoadedOnce: runtime.actions.hasLoadedOnce,
            isReachable: isReachable
        )
    }

    private var cue: WorkspaceStartupCreationCue {
        WorkspaceStartupCreationCue.resolve(
            configuration: resolvedConfiguration,
            validation: validation
        )
    }

    private var primaryIndicator: SheetPrimaryIndicator? {
        guard coordinator == nil, !isSubmitting else { return nil }
        switch cue {
        case .none:
            return nil
        case .configured:
            return SheetPrimaryIndicator(
                systemImage: "bolt.fill",
                color: .accentColor,
                accessibilityLabel: "Starts configured Sessions"
            )
        case .defaultUnavailable:
            return SheetPrimaryIndicator(
                systemImage: "exclamationmark.triangle.fill",
                color: .orange,
                accessibilityLabel: "Configured Default Session is unavailable."
            )
        }
    }

    private var primaryLabel: String {
        coordinator?.primaryActionTitle ?? "Create Workspace"
    }

    private var secondaryLabel: String? {
        coordinator?.secondaryActionTitle
    }

    private var secondaryAction: (() -> Void)? {
        guard secondaryLabel != nil else { return nil }
        return { coordinator?.performSecondaryAction() }
    }

    private var isBusy: Bool {
        isSubmitting || coordinator?.isInProgress == true
    }

    private var isDismissDisabled: Bool {
        coordinator?.isDismissDisabled == true
    }

    private var inputsDisabled: Bool {
        coordinator != nil
    }

    private var canSubmit: Bool {
        if case .failedDefault = coordinator?.state { return true }
        if case .ambiguous = coordinator?.state { return true }
        return !isBusy
            && coordinator?.workspaceRef == nil
            && cue != .defaultUnavailable
            && (selectedProject.map { appModel.canCreateWorkspace(in: $0) } ?? false)
            && !name.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private var availabilityError: String? {
        guard let selectedProject else { return nil }
        guard appModel.canCreateWorkspace(in: selectedProject) else {
            return "This project's connection is unavailable."
        }
        return nil
    }

    private func handlePrimaryAction() {
        if let coordinator, coordinator.primaryActionTitle != nil {
            Task { await coordinator.performPrimaryAction() }
            return
        }
        // A failed creation left nothing durable; a fresh submit starts over.
        coordinator = nil
        Task { await submit() }
    }

    private func submit() async {
        guard let ref = selectedProject,
              appModel.canCreateWorkspace(in: ref),
              let runtime = appModel.runtime(id: ref.connectionID) else {
            submitError = "This project's connection is unavailable."
            return
        }

        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        let configuration = resolvedConfiguration
        if !configuration.entries.isEmpty {
            guard cue != .defaultUnavailable,
                  let plan = WorkspaceStartupLaunchPlan(
                    configuration: configuration,
                    validation: validation
                  )
            else { return }
            submitError = nil
            let newCoordinator = WorkspaceStartupCoordinator(
                connectionID: ref.connectionID,
                plan: plan,
                operations: .init(
                    createWorkspace: { projectID, name in
                        try await runtime.workspaces.create(
                            projectID: projectID,
                            name: name
                        )
                    },
                    startSession: { request in
                        try await runtime.sessions.start(request)
                    },
                    refreshSessions: {
                        await runtime.sessions.refresh()
                    }
                ),
                onActivate: { workspaceRef, sessionRef in
                    dismiss()
                    onCreated(workspaceRef, sessionRef)
                },
                onNotice: onNotice
            )
            coordinator = newCoordinator
            await newCoordinator.start(projectID: ref.projectID, name: trimmedName)
            return
        }

        isSubmitting = true
        defer { isSubmitting = false }
        do {
            let workspace = try await runtime.workspaces.create(
                projectID: ref.projectID,
                name: trimmedName
            )
            submitError = nil
            dismiss()
            onCreated(
                WorkspaceRef(connectionID: ref.connectionID, workspaceID: workspace.id),
                nil
            )
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
