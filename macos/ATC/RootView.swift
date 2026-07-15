import SwiftUI
import ATCAPI

/// One stable window-root split view. Navigators replace only the leading
/// column while the terminal stack remains mounted in the detail column.
@MainActor
struct RootView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    /// The one store ATCApp creates and loads; previews and hosting tests
    /// pass an unloaded throwaway explicitly.
    let configStore: KeyboardConfigStore

    var body: some View {
        KeyboardRoutingContainer(
            appModel: appModel,
            windowState: windowState,
            configStore: configStore
        ) {
            rootContent
        }
    }

    private var rootContent: some View {
        @Bindable var windowState = windowState
        return NavigationSplitView(columnVisibility: $windowState.columnVisibility) {
            NavigatorSidebar()
        } detail: {
            mainContent
                .inspector(isPresented: $windowState.isInspectorPresented) {
                    inspectorContent
                }
        }
        .navigationTitle("atc")
        .navigationSubtitle(activeRuntime?.record.name ?? "")
        .toolbar {
            if windowState.selectedContent != .dashboard {
                ToolbarItem(placement: .navigation) {
                    Button {
                        windowState.showDashboard()
                    } label: {
                        Label("Dashboard", systemImage: "chevron.left")
                    }
                    .labelStyle(.iconOnly)
                    .help("Show Dashboard")
                    .keyboardShortcut(.upArrow, modifiers: .command)
                }
            }
            ToolbarItem(placement: .principal) {
                WorkspaceSwitcher()
            }
            if windowState.activeWorkspace != nil,
               windowState.selectedContent != .dashboard {
                ToolbarItemGroup {
                    WorkspaceActionsMenu()
                        .labelStyle(.iconOnly)
                }
            }
        }
        .sheet(isPresented: $windowState.isCreateProjectPresented) {
            CreateProjectSheet()
        }
        .sheet(item: $windowState.createWorkspaceContext) { context in
            CreateWorkspaceSheet(context: context) { ref in
                _ = windowState.activateWorkspace(ref, in: appModel)
            }
        }
        .sheet(item: $windowState.startSessionKind) { kind in
            if let ref = windowState.activeWorkspace {
                StartWorkspaceSessionSheet(kind: kind, workspaceRef: ref) { newRef in
                    _ = windowState.selectSession(newRef, in: appModel)
                }
            }
        }
        .onChange(of: appModel.windowNavigationSnapshot(), initial: true) {
            windowState.reconcile(in: appModel)
        }
    }

    private var mainContent: some View {
        ZStack {
            SessionContentView(
                selectedRef: visibleSessionRef,
                selectedSession: visibleSession,
                emptyState: workspaceEmptyActions
            )
            if windowState.selectedContent == .dashboard {
                DashboardView(
                    onOpenWorkspace: { _ = windowState.activateWorkspace($0, in: appModel) },
                    onCreateWorkspace: { ref in
                        windowState.createWorkspaceContext = .init(mode: .fixed(ref))
                    },
                    onCreateProject: { windowState.isCreateProjectPresented = true },
                    onWorkspaceDeleted: { windowState.forgetSelection(for: $0) }
                )
                .background()
            }
        }
    }

    @ViewBuilder
    private var inspectorContent: some View {
        if windowState.hasInspectorTarget(in: appModel),
           let ref = visibleSessionRef,
           let session = visibleSession,
           let client = appModel.runtime(id: ref.connectionID)?.client {
            SessionDetailView(session: session, client: client)
                .inspectorColumnWidth(min: 260, ideal: 320)
        }
    }

    private var visibleSessionRef: SessionRef? {
        windowState.selectedSession
    }

    private var visibleSession: Session? {
        visibleSessionRef.flatMap { appModel.session(for: $0) }
    }

    private var activeRuntime: ConnectionRuntime? {
        windowState.activeWorkspace.flatMap { appModel.runtime(id: $0.connectionID) }
    }

    private var workspaceEmptyActions: SessionContentView.EmptyStateActions? {
        guard case .workspace(let ref) = windowState.selectedContent,
              ref == windowState.activeWorkspace,
              let runtime = appModel.runtime(id: ref.connectionID)
        else { return nil }
        let isEmpty = !runtime.sessions.sessions.contains { $0.belongs(to: ref) }
        guard isEmpty else { return nil }
        return .init(
            newSession: { windowState.startSessionKind = .agentSession },
            newTerminal: { windowState.startSessionKind = .terminal },
            creationEnabled: windowState.canStartSession(in: appModel)
        )
    }
}

#Preview {
    RootView(configStore: KeyboardConfigStore())
        .environment(AppModel.preview())
        .environment(WindowState())
        .preferredColorScheme(.dark)
}
