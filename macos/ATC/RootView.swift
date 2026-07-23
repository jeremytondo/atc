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
        // The sidebar's glass composites over the window's base layer, which
        // otherwise varies with what the window contains (it brightens when a
        // Metal-backed terminal surface is visible — ATC-45). Pinning the
        // base layer to the canvas keeps the glass itself but gives it one
        // constant color to rest on.
        .containerBackground(AppColors.canvas, for: .window)
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
        .sheet(item: $windowState.createWorkspaceContext, onDismiss: {
            windowState.presentPendingWorkspaceStartupEditor()
        }) { context in
            CreateWorkspaceSheet(
                context: context,
                onCreated: { workspaceRef, sessionRef in
                    guard windowState.activateWorkspace(workspaceRef, in: appModel) else {
                        return
                    }
                    if let sessionRef {
                        _ = windowState.selectSession(sessionRef, in: appModel)
                    }
                },
                onNotice: { windowState.startupNotice = $0 },
                onEditStartupSettings: {
                    windowState.editWorkspaceStartupAfterCreateSheetDismisses($0)
                }
            )
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
        .sheet(item: $windowState.workspaceStartupProject) { ref in
            WorkspaceStartupEditorSheet(target: .project(ref))
        }
        .onChange(of: appModel.windowNavigationSnapshot(), initial: true) {
            appModel.reconcileTerminalLifecycle()
            windowState.reconcile(in: appModel)
        }
        .overlay(alignment: .top) {
            if let notice = windowState.startupNotice {
                StartupNoticeBanner(notice: notice) {
                    windowState.startupNotice = nil
                }
                .padding(Spacing.md)
                .transition(.move(edge: .top).combined(with: .opacity))
            }
        }
        .animation(.snappy, value: windowState.startupNotice?.id)
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
        .background(AppColors.canvas.ignoresSafeArea(.container, edges: .top))
    }

    @ViewBuilder
    private var inspectorContent: some View {
        if windowState.hasInspectorTarget(in: appModel),
           let session = visibleSession {
            SessionDetailView(session: session)
                .inspectorColumnWidth(min: 260, ideal: 320)
        }
    }

    private var visibleSessionRef: SessionRef? {
        windowState.selectedSession
    }

    private var visibleSession: Session? {
        visibleSessionRef.flatMap { appModel.session(for: $0) }
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

private struct StartupNoticeBanner: View {
    let notice: StartupNotice
    let onDismiss: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: Spacing.md) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(.orange)
            VStack(alignment: .leading, spacing: Spacing.xs) {
                Text("Workspace Startup — \(notice.workspaceName)")
                    .font(.callout.weight(.semibold))
                ForEach(notice.messages, id: \.self) {
                    Text($0)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: Spacing.md)
            Button("Dismiss", systemImage: "xmark", action: onDismiss)
                .labelStyle(.iconOnly)
                .buttonStyle(.borderless)
        }
        .padding(Spacing.md)
        .frame(maxWidth: 560)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
        .overlay {
            RoundedRectangle(cornerRadius: 12)
                .stroke(Color(nsColor: .separatorColor).opacity(0.5))
        }
        .shadow(radius: 8, y: 3)
        .accessibilityElement(children: .contain)
    }
}

#Preview {
    RootView(configStore: ConfigurationStore())
        .environment(AppModel.preview())
        .environment(WindowState())
        .preferredColorScheme(.dark)
}
