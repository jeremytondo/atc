import ATCAppServerAPI
import SwiftUI

/// App-wide Project management rendered as a main-content destination inside
/// the stable window split view: one section per Connection, one card per
/// Project. Deletion cascades server-side to everything the Project owns, so
/// the confirmation spells out the counts before anything is sent.
struct DashboardView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var renamingProject: DashboardModel.ProjectCard?
    @State private var renameDraft = ""
    @State private var deletingProject: DashboardModel.ProjectCard?
    @State private var actionError: String?

    var body: some View {
        let model = DashboardModel(
            inputs: appModel.runtimes.map(DashboardModel.ConnectionInput.init(runtime:))
        )
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Spacing.xxl) {
                ForEach(model.sections) { section in
                    connectionSection(section)
                }
            }
            .frame(maxWidth: 1_100, alignment: .leading)
            .padding(Spacing.xxl)
            .frame(maxWidth: .infinity, alignment: .top)
        }
        .overlay {
            if appModel.runtimes.isEmpty {
                noConnectionsState
            } else if model.totalProjectCount == 0 && allProjectsLoaded {
                noProjectsState
            }
        }
        .renameAlert("Rename Project", item: $renamingProject, draft: $renameDraft) { card, name in
            if let store = appModel.runtime(id: card.ref.connectionID)?.projects {
                appModel.run(on: card.ref.connectionID, reporting: $actionError) {
                    try await store.rename(id: card.project.id, name: name)
                }
            }
        }
        .confirmationDialog(
            "Delete Project “\(deletingProject?.project.name ?? "")”?",
            isPresented: Binding(presented: $deletingProject)
        ) {
            Button("Delete Project", role: .destructive) {
                if let card = deletingProject,
                   let store = appModel.runtime(id: card.ref.connectionID)?.projects {
                    appModel.run(on: card.ref.connectionID, reporting: $actionError) {
                        try await store.delete(id: card.project.id)
                    }
                }
            }
        } message: {
            // Counts are the visible (active/standalone) ones; the catch-all
            // phrase covers archived and ended residue the stores don't
            // surface, so it must read as an addition, not a subset.
            Text("""
            Also deletes its \(countLabel(deletingProject?.activeThreadCount ?? 0, "thread")) and \
            \(countLabel(deletingProject?.standaloneTerminalCount ?? 0, "terminal")), plus \
            any archived or ended ones. The project directory is never touched.
            """)
        }
        .actionErrorAlert($actionError)
    }

    // MARK: - Connection section

    private func connectionSection(_ section: DashboardModel.Section) -> some View {
        let reachability = appModel.reachability(of: section.connectionID)
        return VStack(alignment: .leading, spacing: Spacing.md) {
            HStack(spacing: Spacing.sm) {
                StatusDot(reachability: reachability)
                Text(section.connectionName)
                    .font(.title3.weight(.semibold))
                Text(section.contextLabel)
                    .foregroundStyle(.tertiary)
                Spacer()
                if reachability == .unreachable {
                    Button("Retry", systemImage: "arrow.clockwise") {
                        if let runtime = appModel.runtime(id: section.connectionID) {
                            Task { await runtime.refresh() }
                        }
                    }
                    .labelStyle(.iconOnly)
                    .buttonStyle(.borderless)
                    .help("Retry \(section.connectionName)")
                }
            }

            if section.cards.isEmpty {
                emptyProjectsCard(section)
            } else {
                LazyVGrid(
                    columns: [GridItem(.adaptive(minimum: 280), spacing: Spacing.md)],
                    alignment: .leading,
                    spacing: Spacing.md
                ) {
                    ForEach(section.cards) { card in
                        projectCard(card, canMutate: appModel.canMutate(connectionID: section.connectionID))
                    }
                }
            }
        }
    }

    // MARK: - Project card

    private func projectCard(_ card: DashboardModel.ProjectCard, canMutate: Bool) -> some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            HStack(spacing: Spacing.sm) {
                Text(card.project.name)
                    .font(.headline)
                    .lineLimit(1)
                Spacer(minLength: Spacing.sm)
                if card.needsInputCount > 0 {
                    Label("\(card.needsInputCount)", systemImage: "exclamationmark.bubble.fill")
                        .font(.caption.weight(.semibold))
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(Color.accentColor.opacity(0.22), in: Capsule())
                        .foregroundStyle(Color.accentColor)
                        .help("\(card.needsInputCount) threads need input")
                }
            }

            Text(card.project.defaultWorkingDirectory)
                .font(.caption.monospaced())
                .foregroundStyle(.tertiary)
                .lineLimit(1)
                .truncationMode(.head)

            Text("\(countLabel(card.activeThreadCount, "thread")) · \(countLabel(card.standaloneTerminalCount, "terminal"))")
                .font(.caption)
                .foregroundStyle(.secondary)

            Button("New Thread", systemImage: "plus.bubble") {
                windowState.newThreadContext = NewThreadContext(projectRef: card.ref)
            }
            .buttonStyle(.bordered)
            .disabled(!canMutate)
        }
        .padding(Spacing.lg)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(nsColor: .controlBackgroundColor), in: cardShape)
        .overlay { cardShape.stroke(Color(nsColor: .separatorColor), lineWidth: 1) }
        .contextMenu {
            Button("New Thread…", systemImage: "plus.bubble") {
                windowState.newThreadContext = NewThreadContext(projectRef: card.ref)
            }
            .disabled(!canMutate)
            Button("Rename…", systemImage: "pencil") {
                renameDraft = card.project.name
                renamingProject = card
            }
            .disabled(!canMutate)
            Divider()
            Button("Delete Project…", systemImage: "trash", role: .destructive) {
                deletingProject = card
            }
            .disabled(!canMutate)
        }
    }

    private func emptyProjectsCard(_ section: DashboardModel.Section) -> some View {
        HStack(spacing: Spacing.md) {
            Text("No projects yet")
                .font(.callout)
                .foregroundStyle(.tertiary)
            Spacer()
            Button("New Project", systemImage: "plus") {
                windowState.isCreateProjectPresented = true
            }
            .buttonStyle(.bordered)
            .disabled(!appModel.canMutate(connectionID: section.connectionID))
        }
        .padding(Spacing.lg)
        .frame(maxWidth: .infinity)
        .overlay {
            cardShape.stroke(
                Color(nsColor: .separatorColor),
                style: StrokeStyle(lineWidth: 1, dash: [5, 4])
            )
        }
    }

    // MARK: - Empty states

    private var noConnectionsState: some View {
        ContentUnavailableView {
            Label("Add a Connection", systemImage: "network.slash")
        } description: {
            Text("Connect to an atc server in Settings to see its projects and threads here.")
        } actions: {
            SettingsLink {
                Text("Open Settings")
            }
        }
        .background(AppColors.canvas)
    }

    private var noProjectsState: some View {
        ContentUnavailableView {
            Label("No Projects", systemImage: "folder")
        } description: {
            Text("Create a project to start working in a codebase.")
        } actions: {
            Button("New Project") { windowState.isCreateProjectPresented = true }
                .disabled(!appModel.runtimes.contains { appModel.canMutate(connectionID: $0.id) })
        }
        .background(AppColors.canvas)
    }

    // MARK: - Helpers

    /// "No projects yet" is an affirmative statement; an unreachable server
    /// must never make it (its section shows red + the toolbar warns).
    private var allProjectsLoaded: Bool {
        appModel.runtimes.allSatisfy(\.projects.isResolved)
    }

    private var cardShape: RoundedRectangle {
        RoundedRectangle(cornerRadius: Radius.card, style: .continuous)
    }

    private func countLabel(_ count: Int, _ noun: String) -> String {
        "\(count) \(count == 1 ? noun : noun + "s")"
    }
}

#Preview {
    DashboardView()
        .environment(AppModel.preview())
        .environment(WindowState())
        .preferredColorScheme(.dark)
}
