import SwiftUI
import CockpitAPI

/// Single-window shell: project sidebar, session content on the right.
struct ContentView: View {
    @Environment(AppModel.self) private var appModel
    @State private var selectedSessionID: String?
    @State private var searchText = ""
    @State private var showCreateProject = false
    /// Non-nil presents the New Session sheet scoped to that project.
    @State private var newSessionProject: Project?

    var body: some View {
        NavigationSplitView {
            ProjectSidebarView(
                selection: $selectedSessionID,
                searchText: searchText,
                connectedIDs: Set(appModel.terminals.keys),
                newSessionProject: $newSessionProject,
                onCreateProject: { showCreateProject = true }
            )
            .navigationSplitViewColumnWidth(min: 220, ideal: 280)
            .searchable(text: $searchText, placement: .sidebar, prompt: "Search projects and sessions")
            .toolbar {
                ToolbarItemGroup {
                    Button {
                        showCreateProject = true
                    } label: {
                        Label("New Project", systemImage: "folder.badge.plus")
                    }
                    .help("New project")
                    .keyboardShortcut("n", modifiers: [.command, .shift])
                    Button {
                        newSessionProject = selectedProject
                    } label: {
                        Label("New Session", systemImage: "plus")
                    }
                    .help(selectedProject == nil
                        ? "Select a project session first, or use a project's + button"
                        : "New session in \(selectedProject!.name)")
                    .disabled(selectedProject == nil)
                    .keyboardShortcut("n", modifiers: .command)
                    Toggle(isOn: showArchivedBinding) {
                        Label("Show Archived", systemImage: "archivebox")
                    }
                    .help("Show archived projects and sessions")
                    Button {
                        Task {
                            await appModel.projects.refresh()
                            await appModel.sessions.refresh()
                        }
                    } label: {
                        Label("Refresh", systemImage: "arrow.clockwise")
                    }
                    .help("Refresh projects and sessions")
                    .keyboardShortcut("r", modifiers: .command)
                }
            }
        } detail: {
            SessionContentView(selectedSession: selectedSession)
        }
        .sheet(isPresented: $showCreateProject) {
            CreateProjectSheet()
        }
        .sheet(item: $newSessionProject) { project in
            CreateSessionSheet(project: project) { newSessionID in
                selectedSessionID = newSessionID
            }
        }
        .onChange(of: selectedSessionID) { attachSelectedIfNeeded() }
        .onChange(of: appModel.sessions.sessions) { attachSelectedIfNeeded() }
        .task {
            await appModel.sessions.pollLoop()
        }
        .task {
            await appModel.projects.pollLoop()
        }
    }

    private var selectedSession: Session? {
        selectedSessionID.flatMap { appModel.sessions.session(id: $0) }
    }

    /// Project context for ⌘N: the selected session's project.
    private var selectedProject: Project? {
        guard let ref = selectedSession?.project else { return nil }
        return appModel.projects.project(id: ref.id)
            ?? Project(
                id: ref.id,
                name: ref.name,
                workingDir: ref.workingDir,
                createdAt: .now,
                updatedAt: .now,
                archivedAt: ref.archivedAt
            )
    }

    /// One toggle drives both stores' archived filters.
    private var showArchivedBinding: Binding<Bool> {
        Binding(
            get: { appModel.sessions.includeArchived },
            set: { on in
                appModel.sessions.includeArchived = on
                appModel.projects.includeArchived = on
            }
        )
    }

    /// Selecting an attachable session auto-attaches — no explicit Connect
    /// step. Also fires when a selected starting session becomes attachable.
    private func attachSelectedIfNeeded() {
        if let session = selectedSession, session.attachable {
            appModel.attachIfNeeded(to: session)
        }
    }
}

/// Content area: compact header above the terminal stack. The TerminalPane
/// is ALWAYS in the hierarchy (even with nothing selected) so live surfaces
/// and their WebSockets survive any sidebar navigation; non-terminal states
/// draw an opaque cover over it.
struct SessionContentView: View {
    @Environment(AppModel.self) private var appModel
    let selectedSession: Session?

    @State private var showInspector = false

    var body: some View {
        VStack(spacing: 0) {
            if let session = selectedSession {
                SessionHeaderBar(session: session, showInspector: $showInspector)
                Divider()
            }
            ZStack {
                TerminalPane(visibleSessionID: selectedSession?.id)
                cover
            }
        }
        .inspector(isPresented: $showInspector) {
            if let session = selectedSession {
                SessionDetailView(session: session)
                    .inspectorColumnWidth(min: 260, ideal: 320)
            }
        }
        .navigationTitle(selectedSession?.displayName ?? "AtelierCode")
    }

    @ViewBuilder
    private var cover: some View {
        if let session = selectedSession {
            if let controller = appModel.terminals[session.id] {
                // Terminal visible; only the phase banner floats on top.
                TerminalStatusBanner(controller: controller) {
                    appModel.disconnectTerminal(id: session.id)
                }
            } else if session.attachable {
                // Normally auto-attach creates the controller in the same
                // update; this cover only lingers after an explicit
                // Disconnect, so offer the way back in.
                ContentUnavailableView {
                    Label("Not Connected", systemImage: "cable.connector.slash")
                } description: {
                    Text("The session is running on the server.")
                } actions: {
                    Button("Connect") { appModel.attachIfNeeded(to: session) }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background()
            } else {
                SessionDetailView(session: session)
                    .background()
            }
        } else {
            ContentUnavailableView(
                "No Session Selected",
                systemImage: "terminal",
                description: Text("Select a session in the sidebar.")
            )
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background()
        }
    }
}

/// Compact header: name, status, session actions.
struct SessionHeaderBar: View {
    @Environment(AppModel.self) private var appModel
    let session: Session
    @Binding var showInspector: Bool

    @State private var confirmStop = false
    @State private var confirmArchive = false
    @State private var actionError: String?

    private var isConnected: Bool {
        appModel.terminals[session.id] != nil
    }

    private var canStop: Bool {
        session.status == .running || session.status == .starting
    }

    /// Mirror the server rule: archive only after the session ended.
    private var canArchive: Bool {
        !session.isArchived && (session.status == .terminated || session.status == .failed)
    }

    var body: some View {
        HStack(spacing: 12) {
            StatusBadge(session: session, showLabel: true)
            Text(session.displayName)
                .font(.headline)
                .lineLimit(1)
            Text(session.workingDir)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.head)
            Spacer()
            if isConnected {
                Button("Disconnect", systemImage: "cable.connector.slash") {
                    appModel.disconnectTerminal(id: session.id)
                }
                .help("Detach from the session (it keeps running)")
            }
            if canStop {
                Button("Stop", systemImage: "stop.circle") {
                    confirmStop = true
                }
                .help("Terminate this session")
            }
            if !session.isArchived {
                Button("Archive", systemImage: "archivebox") {
                    confirmArchive = true
                }
                .disabled(!canArchive)
                .help(canArchive ? "Archive this session" : "Stop the session before archiving")
            }
            if isConnected {
                Button("Info", systemImage: "sidebar.trailing") {
                    showInspector.toggle()
                }
                .help("Show session metadata")
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .confirmationDialog(
            "Stop “\(session.displayName)”?",
            isPresented: $confirmStop
        ) {
            Button("Stop Session", role: .destructive) {
                Task { await run { try await appModel.sessions.terminate(id: session.id) } }
            }
        } message: {
            Text("The agent process will be terminated. The session record is kept until archived.")
        }
        .confirmationDialog(
            "Archive “\(session.displayName)”?",
            isPresented: $confirmArchive
        ) {
            Button("Archive Session") {
                Task { await run { try await appModel.sessions.archive(id: session.id) } }
            }
        } message: {
            Text("Archived sessions are hidden behind the archived filter.")
        }
        .alert("Session Action Failed", isPresented: Binding(
            get: { actionError != nil },
            set: { if !$0 { actionError = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(actionError ?? "")
        }
    }

    private func run(_ operation: () async throws -> Void) async {
        do {
            try await operation()
        } catch {
            actionError = error.localizedDescription
        }
    }
}

#Preview {
    ContentView()
        .environment(AppModel(client: MockCockpitClient()))
        .preferredColorScheme(.dark)
}
