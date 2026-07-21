import SwiftUI
import ATCAPI

/// The New Session / New Terminal sheet, always scoped to the Active
/// Workspace. New Session picks among enabled Agent Actions; New Terminal
/// offers the Interactive Shell (default) plus enabled general Actions. No
/// prompt field, no params UI, no Environment picker — the server default
/// Environment is used.
struct StartWorkspaceSessionSheet: View {
    /// Sentinel Picker tag for the Interactive Shell (a nil-action start).
    private static let interactiveShellTag = ""

    @Environment(AppModel.self) private var appModel
    @Environment(\.dismiss) private var dismiss
    let kind: StartSessionKind
    let workspaceRef: WorkspaceRef
    /// Called with the new Session's ref so the window can select it.
    var onStarted: (SessionRef) -> Void = { _ in }

    @State private var selectedActionName = ""
    @State private var name = ""
    @State private var isSubmitting = false
    @State private var submitError: String?

    private var runtime: ConnectionRuntime? {
        appModel.runtime(id: workspaceRef.connectionID)
    }

    private var actionsStore: ActionsStore? { runtime?.actions }

    /// The pickable actions for this sheet's kind, enabled only.
    private var choices: [ATCAction] {
        let enabled = (actionsStore?.actions ?? []).filter(\.enabled)
        switch kind {
        case .agentSession: return enabled.filter(\.isAgent)
        case .terminal: return enabled.filter { !$0.isAgent }
        }
    }

    private var isInteractiveShell: Bool {
        kind == .terminal && selectedActionName == Self.interactiveShellTag
    }

    private var selectedAction: ATCAction? {
        choices.first { $0.name == selectedActionName }
    }

    /// The actions list is polled with the runtime; a load failure with
    /// nothing cached shows the standard inline error with Retry.
    private var loadError: String? {
        guard let store = actionsStore else {
            return "This workspace's connection is no longer configured."
        }
        guard store.actions.isEmpty else { return nil }
        return store.lastError
    }

    private var availabilityError: String? {
        guard runtime != nil else {
            return "This workspace's connection is no longer configured."
        }
        guard appModel.canStartSession(in: workspaceRef) else {
            return "This workspace's connection is unavailable."
        }
        return nil
    }

    var body: some View {
        SheetScaffold(
            title: kind == .agentSession ? "New Session" : "New Terminal",
            systemImage: kind == .agentSession ? "sparkles" : "terminal",
            primaryLabel: "Start",
            isBusy: isSubmitting,
            canSubmit: canSubmit,
            onCancel: { dismiss() },
            onSubmit: { Task { await submit() } }
        ) {
            Section {
                picker
                TextField("Name (optional)", text: $name)
            }

            if let message = submitError ?? availabilityError ?? loadError {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.red)
                        .font(.callout)
                    if loadError != nil {
                        Button("Retry") {
                            Task { await actionsStore?.refresh() }
                        }
                    }
                }
            }
        }
        .frame(width: 460, height: 280)
        .onAppear { preselect() }
        .onChange(of: choices.map(\.name)) { preselect() }
    }

    @ViewBuilder
    private var picker: some View {
        switch kind {
        case .agentSession:
            Picker("Agent", selection: $selectedActionName) {
                // Actions load async; the "" selection needs a matching
                // tag until then or AppKit logs an invalid selection.
                if choices.isEmpty {
                    Text(actionsStore?.hasLoadedOnce == true ? "No agents enabled" : "Loading…")
                        .tag("")
                }
                ForEach(choices) { action in
                    Text(action.displayLabel).tag(action.name)
                }
            }
        case .terminal:
            Picker("Run", selection: $selectedActionName) {
                Text("Interactive Shell").tag(Self.interactiveShellTag)
                ForEach(choices) { action in
                    Text(action.displayLabel).tag(action.name)
                }
            }
        }
    }

    /// New Session preselects the first enabled agent; New Terminal
    /// defaults to the Interactive Shell.
    private func preselect() {
        guard kind == .agentSession, selectedAction == nil else { return }
        selectedActionName = choices.first?.name ?? ""
    }

    private var canSubmit: Bool {
        guard !isSubmitting, appModel.canStartSession(in: workspaceRef) else { return false }
        switch kind {
        case .agentSession: return selectedAction != nil
        case .terminal: return isInteractiveShell || selectedAction != nil
        }
    }

    private func submit() async {
        guard appModel.canStartSession(in: workspaceRef), let runtime else {
            submitError = "This workspace's connection is unavailable."
            return
        }
        isSubmitting = true
        defer { isSubmitting = false }
        do {
            let trimmedName = name.trimmingCharacters(in: .whitespaces)
            let request = StartSessionRequest(
                workspaceId: workspaceRef.workspaceID,
                action: isInteractiveShell ? nil : selectedAction?.name,
                name: trimmedName.isEmpty ? nil : trimmedName
            )
            let detail = try await runtime.sessions.start(request)
            submitError = nil
            dismiss()
            onStarted(SessionRef(connectionID: runtime.id, sessionID: detail.id))
        } catch {
            submitError = error.localizedDescription
        }
    }
}

#Preview("New Session") {
    let appModel = AppModel.preview()
    StartWorkspaceSessionSheet(
        kind: .agentSession,
        workspaceRef: WorkspaceRef(
            connectionID: appModel.runtimes.first!.id,
            workspaceID: "wsp_parser"
        )
    )
    .environment(appModel)
    .preferredColorScheme(.dark)
}

#Preview("New Terminal") {
    let appModel = AppModel.preview()
    StartWorkspaceSessionSheet(
        kind: .terminal,
        workspaceRef: WorkspaceRef(
            connectionID: appModel.runtimes.first!.id,
            workspaceID: "wsp_parser"
        )
    )
    .environment(appModel)
    .preferredColorScheme(.dark)
}
