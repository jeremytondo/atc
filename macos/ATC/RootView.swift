import ATCAppServerAPI
import SwiftUI

/// One stable window-root split view. The sidebar rebuilds freely while the
/// terminal stack stays mounted in the detail column, so navigation never
/// tears down a surface or drops an attach.
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
                contextTitle
            }
            // The design's exceptional-state surface: needs-input and
            // Connection-unreachable live here; healthy states show nothing.
            ToolbarItem(placement: .status) {
                exceptionalStatus
            }
            ToolbarItem(placement: .primaryAction) {
                Toggle(isOn: $windowState.isInspectorPresented) {
                    Label("Inspector", systemImage: "sidebar.trailing")
                        .labelStyle(.iconOnly)
                }
                .toggleStyle(.button)
                // Closing is always allowed; opening needs a thread to show.
                .disabled(!windowState.isInspectorPresented && selectedThread == nil)
                .help(windowState.isInspectorPresented ? "Hide Inspector" : "Show Inspector")
            }
        }
        .sheet(isPresented: $windowState.isCreateProjectPresented) {
            CreateProjectSheet()
        }
        .sheet(item: $windowState.newThreadContext, onDismiss: {
            windowState.requestTerminalFocus()
        }) { context in
            NewThreadSheet(context: context)
        }
        .sheet(item: $windowState.newTerminalProject, onDismiss: {
            windowState.requestTerminalFocus()
        }) { ref in
            NewTerminalSheet(projectRef: ref)
        }
        .onChange(of: appModel.windowNavigationSnapshot(), initial: true) {
            appModel.reconcileTerminalLifecycle()
            windowState.reconcile(in: appModel)
        }
    }

    private var mainContent: some View {
        ZStack {
            ThreadContentView()
            if windowState.selectedContent == .dashboard {
                DashboardView()
                    .background(AppColors.canvas)
            }
        }
        .background(AppColors.canvas.ignoresSafeArea(.container, edges: .top))
    }

    @ViewBuilder
    private var contextTitle: some View {
        switch windowState.selectedContent {
        case .dashboard:
            Text("atc")
                .font(.headline)
        case .thread(let ref):
            if let thread = appModel.thread(for: ref) {
                Label {
                    Text("\(thread.displayName) · \(projectName(for: ref.connectionID, projectID: thread.projectId))")
                        .font(.headline)
                        .lineLimit(1)
                } icon: {
                    Image(systemName: thread.agentId.systemImage)
                        .accessibilityLabel(thread.agentId.displayName)
                }
            }
        case .terminal(let ref):
            if let terminal = appModel.terminal(for: ref) {
                Label(terminal.displayName, systemImage: "terminal")
                    .font(.headline)
                    .lineLimit(1)
            }
        }
    }

    @ViewBuilder
    private var exceptionalStatus: some View {
        HStack(spacing: Spacing.md) {
            if let ref = selectedThread,
               appModel.thread(for: ref)?.activityState == .needsInput {
                Label("Needs input", systemImage: "exclamationmark.bubble.fill")
                    .font(.callout.weight(.medium))
                    .foregroundStyle(Color.accentColor)
                    .help("The agent is waiting for your input")
            }
            if let name = unreachableConnectionName {
                Label("\(name) unreachable", systemImage: "wifi.exclamationmark")
                    .font(.callout.weight(.medium))
                    // Orange like the app's other warning banners: this is a
                    // persistent surface, softer than the red state dot.
                    .foregroundStyle(.orange)
                    // The attach WebSocket is deliberately independent of
                    // this state — an open terminal keeps working — so the
                    // copy only claims what canMutate actually gates.
                    .help("Showing cached content; creating and organizing are disabled until the connection returns")
            }
        }
    }

    /// The first unreachable Connection's display name; nil while all are
    /// healthy or still unknown (launch must not flash a warning).
    private var unreachableConnectionName: String? {
        appModel.runtimes.first { $0.reachability == .unreachable }?.record.name
    }

    @ViewBuilder
    private var inspectorContent: some View {
        if let ref = selectedThread, let thread = appModel.thread(for: ref) {
            ThreadInspectorView(
                thread: thread,
                projectName: projectName(for: ref.connectionID, projectID: thread.projectId)
            )
            .inspectorColumnWidth(min: 260, ideal: 320)
        }
    }

    private var selectedThread: ThreadRef? {
        windowState.selectedThread
    }

    private func projectName(for connectionID: UUID, projectID: String) -> String {
        appModel.runtime(id: connectionID)?.projects.project(id: projectID)?.name
            ?? "Unknown Project"
    }
}

#Preview {
    RootView(configStore: ConfigurationStore())
        .environment(AppModel.preview())
        .environment(WindowState())
        .preferredColorScheme(.dark)
}
