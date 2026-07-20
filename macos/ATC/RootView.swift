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
    let configStore: ConfigurationStore

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
        .toolbar(removing: .title)
        .toolbarBackgroundVisibility(.hidden, for: .windowToolbar)
        .toolbar {
            ToolbarItem(placement: .principal) {
                WorkspaceSwitcher()
            }
            ToolbarItem(placement: .primaryAction) {
                Toggle(isOn: $windowState.isInspectorPresented) {
                    Label("Inspector", systemImage: "sidebar.trailing")
                        .labelStyle(.iconOnly)
                }
                .toggleStyle(.button)
                // Closing is always allowed; opening needs a session to show.
                .disabled(!windowState.isInspectorPresented
                    && !windowState.hasInspectorTarget(in: appModel))
                .help(windowState.isInspectorPresented ? "Hide Inspector" : "Show Inspector")
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
        .sheet(item: $windowState.startSessionKind, onDismiss: {
            windowState.requestTerminalFocus()
        }) { kind in
            if let ref = windowState.activeWorkspace {
                StartWorkspaceSessionSheet(kind: kind, workspaceRef: ref) { newRef in
                    _ = windowState.selectSession(newRef, in: appModel)
                }
            }
        }
        .onChange(of: appModel.windowNavigationSnapshot(), initial: true) {
            appModel.reconcileTerminalLifecycle()
            windowState.reconcile(in: appModel)
        }
    }

    private var mainContent: some View {
        ZStack {
            SessionContentView(
                selectedRef: visibleSessionRef,
                selectedSession: visibleSession,
                terminalFocusRequest: windowState.terminalFocusRequest,
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
                .background(AppColors.canvas)
            }
        }
        .background {
            let backingColor = detailBackingColor
            backingColor.color
                .ignoresSafeArea(.container, edges: .top)
                .animation(.easeInOut(duration: 0.2), value: backingColor)
        }
    }

    @ViewBuilder
    private var inspectorContent: some View {
        if windowState.hasInspectorTarget(in: appModel),
           let ref = visibleSessionRef,
           let session = visibleSession,
           let client = appModel.runtime(id: ref.connectionID)?.client {
            SessionDetailView(sessionRef: ref, session: session, client: client)
                .inspectorColumnWidth(min: 260, ideal: 320)
        }
    }

    private var visibleSessionRef: SessionRef? {
        windowState.selectedSession
    }

    private var visibleSession: Session? {
        visibleSessionRef.flatMap { appModel.session(for: $0) }
    }

    private var detailBackingColor: TerminalBackingColor {
        let hasController = visibleSessionRef.map { appModel.terminals[$0] != nil } ?? false
        let showsTerminal = DetailCanvas.showsTerminal(
            isDashboard: windowState.selectedContent == .dashboard,
            session: visibleSession,
            hasController: hasController
        )
        return DetailCanvas.backingColor(
            showsTerminal: showsTerminal,
            preferences: configStore.configuration.terminal
        )
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
    RootView(configStore: ConfigurationStore())
        .environment(AppModel.preview())
        .environment(WindowState())
        .preferredColorScheme(.dark)
}
