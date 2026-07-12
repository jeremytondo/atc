import SwiftUI
import ATCAPI

/// Target for the New Session sheet: a project plus the Connection it
/// lives on, so the sheet routes through the owning runtime.
struct NewSessionContext: Identifiable {
    let connectionID: UUID
    let project: Project

    var id: ProjectRef { ProjectRef(connectionID: connectionID, projectID: project.id) }
}

/// Single-window shell: project sidebar, session content on the right.
struct ContentView: View {
    @Environment(AppModel.self) private var appModel
    @State private var searchText = ""
    @State private var showCreateProject = false
    /// Non-nil presents the New Session sheet scoped to that project.
    @State private var newSessionContext: NewSessionContext?

    var body: some View {
        @Bindable var appModel = appModel
        NavigationSplitView {
            ProjectSidebarView(
                selection: $appModel.selection,
                searchText: searchText,
                connectedRefs: appModel.activelyAttachedRefs,
                newSessionContext: $newSessionContext,
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
                    .disabled(appModel.runtimes.isEmpty)
                    .keyboardShortcut("n", modifiers: [.command, .shift])
                    Button {
                        newSessionContext = newSessionTarget
                    } label: {
                        Label("New Session", systemImage: "plus")
                    }
                    .help(newSessionTarget.map { "New session in \($0.project.name)" }
                        ?? "Select a project session first, or use a project's + button")
                    .disabled(newSessionTarget == nil)
                    .keyboardShortcut("n", modifiers: .command)
                    Button {
                        Task { await appModel.refreshAll() }
                    } label: {
                        Label("Refresh", systemImage: "arrow.clockwise")
                    }
                    .help("Refresh projects and sessions")
                    .keyboardShortcut("r", modifiers: .command)
                }
            }
        } detail: {
            SessionContentView(selectedRef: appModel.selection, selectedSession: selectedSession)
        }
        .sheet(isPresented: $showCreateProject) {
            CreateProjectSheet()
        }
        .sheet(item: $newSessionContext) { context in
            CreateSessionSheet(context: context) { newRef in
                appModel.selection = newRef
            }
        }
        .onChange(of: appModel.selection) { attachSelectedIfNeeded() }
        .onChange(of: selectedSession) { attachSelectedIfNeeded() }
    }

    private var selectedSession: Session? {
        appModel.selection.flatMap { appModel.session(for: $0) }
    }

    /// Project context for ⌘N: the selected session's project, unless it's
    /// archived — the server refuses starts there, so the button matches
    /// the sidebar and disables instead.
    private var newSessionTarget: NewSessionContext? {
        guard let selection = appModel.selection,
              let ref = selectedSession?.project,
              // The derived ref is only {id, name}; the full record (working
              // directory, archive state) must come from the runtime's store.
              let project = appModel.runtime(id: selection.connectionID)?.projects.project(id: ref.id),
              !project.isArchived
        else { return nil }
        return NewSessionContext(connectionID: selection.connectionID, project: project)
    }

    /// Selecting an attachable session auto-attaches — no explicit Connect
    /// step. Also fires when a selected starting session becomes attachable.
    private func attachSelectedIfNeeded() {
        if let ref = appModel.selection, let session = selectedSession, session.attachable {
            appModel.attachIfNeeded(to: session, connectionID: ref.connectionID)
        }
    }
}

/// Content area: compact header above the terminal stack. The TerminalPane
/// is ALWAYS in the hierarchy (even with nothing selected) so live surfaces
/// and their WebSockets survive any sidebar navigation; non-terminal states
/// draw an opaque cover over it.
struct SessionContentView: View {
    @Environment(AppModel.self) private var appModel
    let selectedRef: SessionRef?
    let selectedSession: Session?

    @State private var showInspector = false

    var body: some View {
        VStack(spacing: 0) {
            if let ref = selectedRef, let session = selectedSession {
                SessionHeaderBar(sessionRef: ref, session: session, showInspector: $showInspector)
                Divider()
            }
            ZStack {
                TerminalPane(visibleRef: selectedRef)
                cover
            }
        }
        .inspector(isPresented: $showInspector) {
            if let ref = selectedRef, let session = selectedSession,
               let client = appModel.runtime(id: ref.connectionID)?.client {
                SessionDetailView(session: session, client: client)
                    .inspectorColumnWidth(min: 260, ideal: 320)
            }
        }
        .navigationTitle(selectedSession?.displayName ?? "atc")
    }

    @ViewBuilder
    private var cover: some View {
        if let ref = selectedRef, let session = selectedSession {
            if let controller = appModel.terminals[ref] {
                // Terminal visible; only the phase banner floats on top.
                TerminalStatusBanner(controller: controller) {
                    appModel.disconnectTerminal(ref: ref)
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
                    Button("Connect") {
                        appModel.attachIfNeeded(to: session, connectionID: ref.connectionID)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background()
            } else if let client = appModel.runtime(id: ref.connectionID)?.client {
                SessionDetailView(session: session, client: client)
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
    let sessionRef: SessionRef
    let session: Session
    @Binding var showInspector: Bool

    @State private var confirmStop = false
    @State private var confirmArchive = false
    @State private var actionError: String?

    private var isConnected: Bool {
        appModel.terminals[sessionRef]?.isActivelyAttached == true
    }

    private var canStop: Bool {
        session.status == .running || session.status == .starting
    }

    /// Mirror the server rule: archive only after the session ended.
    private var canArchive: Bool {
        !session.isArchived && (session.status == .terminated || session.status == .failed)
    }

    private var sessionsStore: SessionsStore? {
        appModel.runtime(id: sessionRef.connectionID)?.sessions
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
                    appModel.disconnectTerminal(ref: sessionRef)
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
                Task { await run { try await sessionsStore?.terminate(id: session.id) } }
            }
        } message: {
            Text("The agent process will be terminated. The session record is kept until archived.")
        }
        .confirmationDialog(
            "Archive “\(session.displayName)”?",
            isPresented: $confirmArchive
        ) {
            Button("Archive Session") {
                Task { await run { try await sessionsStore?.archive(id: session.id) } }
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
        .environment(AppModel.preview())
        .preferredColorScheme(.dark)
}
