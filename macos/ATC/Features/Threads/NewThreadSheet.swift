// Form for `POST /threads`. Agent availability is detected, never stored, so
// the sheet refreshes the registry on appear and disables providers the
// server reports as missing — with the server's own actionable reason, which
// is the only place the user learns what to install.

import ATCAppServerAPI
import SwiftUI

struct NewThreadSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    @Environment(\.dismiss) private var dismiss
    let context: NewThreadContext

    @State private var projectRef: ProjectRef?
    @State private var agentID: AgentID = .codex
    @State private var name = ""
    @State private var workingDirectory = ""
    @State private var directoryState: DirectoryCheckState = .idle
    /// Once the user edits the directory, changing project no longer
    /// overwrites it.
    @State private var hasEditedDirectory = false
    @State private var isSubmitting = false
    @State private var submitError: String?

    var body: some View {
        let options = projectOptions
        SheetScaffold(
            title: "New Thread",
            systemImage: "plus.bubble",
            primaryLabel: "Create Thread",
            isBusy: isSubmitting,
            canSubmit: canSubmit,
            onCancel: { windowState.newThreadContext = nil },
            onSubmit: { Task { await submit() } }
        ) {
            Section {
                Picker("Project", selection: Binding(
                    get: { projectRef },
                    set: { selectProject($0, in: options) }
                )) {
                    if projectRef == nil {
                        Text(options.isEmpty ? "No Projects" : "Select Project")
                            .tag(ProjectRef?.none)
                    }
                    ForEach(options) { option in
                        Text(option.label).tag(ProjectRef?.some(option.ref))
                    }
                }
                Picker("Agent", selection: $agentID) {
                    ForEach(agents, id: \.id) { agent in
                        Text(agentLabel(agent))
                            .tag(agent.id)
                            // `.disabled` is inert on picker options; only
                            // `.selectionDisabled` actually blocks selection.
                            .selectionDisabled(!agent.available)
                    }
                }
                TextField("Name (optional)", text: $name)
            }

            Section {
                WorkingDirectoryField(
                    label: "Working Directory",
                    path: Binding(
                        get: { workingDirectory },
                        set: {
                            workingDirectory = $0
                            hasEditedDirectory = true
                        }
                    ),
                    client: selectedRuntime?.client,
                    connectionID: selectedRuntime?.id,
                    state: $directoryState
                )
            } footer: {
                Text("The agent session runs in this directory on the server.")
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
        .frame(width: 480, height: 380)
        .task {
            if projectRef == nil {
                selectProject(context.projectRef ?? options.first?.ref, in: options)
            }
            await selectedRuntime?.agents.refresh()
            adoptFirstAvailableAgent()
        }
    }

    // MARK: - Derived state

    private var projectOptions: [ProjectOption] {
        ThreadListModel(
            inputs: appModel.runtimes.map(ThreadListModel.ConnectionInput.init(runtime:)),
            filter: .all
        ).projects
    }

    private var selectedRuntime: ConnectionRuntime? {
        projectRef.flatMap { appModel.runtime(id: $0.connectionID) }
    }

    /// The Connection's detected registry, or the built-in slugs before the
    /// first detection lands so the picker is never empty.
    private var agents: [Agent] {
        let detected = selectedRuntime?.agents.agents ?? []
        guard detected.isEmpty else { return detected }
        return AgentID.allCases.map {
            Agent(id: $0, available: true, testedVersion: "", tuiObservation: .hooks)
        }
    }

    private var canSubmit: Bool {
        !isSubmitting
            && projectRef != nil
            && directoryState.isAvailable
            && agents.first { $0.id == agentID }?.available == true
    }

    private func agentLabel(_ agent: Agent) -> String {
        guard !agent.available else { return agent.id.displayName }
        return "\(agent.id.displayName) — \(agent.reason ?? "Unavailable")"
    }

    // MARK: - Actions

    private func selectProject(_ ref: ProjectRef?, in options: [ProjectOption]) {
        projectRef = ref
        guard !hasEditedDirectory else { return }
        workingDirectory = options.first { $0.ref == ref }?.project.defaultWorkingDirectory ?? ""
    }

    /// A picker whose selection is disabled cannot be corrected by the user;
    /// move off an unavailable provider once detection says so.
    private func adoptFirstAvailableAgent() {
        guard agents.first(where: { $0.id == agentID })?.available != true,
              let available = agents.first(where: \.available)
        else { return }
        agentID = available.id
    }

    private func submit() async {
        guard let ref = projectRef, let runtime = selectedRuntime else { return }
        isSubmitting = true
        defer { isSubmitting = false }
        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        do {
            let thread = try await runtime.threads.create(.init(
                projectId: ref.projectID,
                agentId: agentID,
                name: trimmedName.isEmpty ? nil : trimmedName,
                workingDirectory: workingDirectory.trimmingCharacters(in: .whitespaces)
            ))
            submitError = nil
            windowState.threadCreated(
                ThreadRef(connectionID: ref.connectionID, threadID: thread.id),
                in: appModel
            )
        } catch {
            submitError = error.localizedDescription
        }
    }
}
