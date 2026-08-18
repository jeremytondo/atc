import ATCAppServerAPI
import SwiftUI

/// One stable window-root split view. The sidebar rebuilds freely while the
/// terminal stack stays mounted in the detail column, so navigation never
/// tears down a surface or drops an attach.
@MainActor
struct RootView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        rootContent
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
            // Plain text, not a control: hide the glass capsule the toolbar
            // would otherwise draw behind the title.
            ToolbarItem(placement: .navigation) {
                contextTitle
            }
            .sharedBackgroundVisibility(.hidden)
            // The design's exceptional-state surface: needs-input and
            // Connection-unreachable live here; healthy states show nothing.
            ToolbarItem(placement: .status) {
                exceptionalStatus
            }
            // Chat | TUI for the displayed thread; the mode is app-wide per
            // thread (AppModel), so two windows on one thread agree.
            ToolbarItem(placement: .principal) {
                if let ref = selectedThread {
                    Picker("View", selection: viewModeBinding(ref)) {
                        Text("Chat").tag(ThreadViewMode.chat)
                        Text("TUI").tag(ThreadViewMode.tui)
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .help("Show the thread as a native chat or its provider TUI")
                }
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
        .sheet(
            item: $windowState.newThreadContext,
            onDismiss: {
                windowState.requestContentFocus()
            }
        ) { context in
            NewThreadSheet(context: context)
        }
        .sheet(
            item: $windowState.newTerminalProject,
            onDismiss: {
                windowState.requestContentFocus()
            }
        ) { ref in
            NewTerminalSheet(projectRef: ref)
        }
        // Returning to the app with a freshly finished thread on screen
        // counts as viewing it (ATC-160) — reconciliation only sees store
        // changes, so activation needs its own hook.
        .onChange(of: scenePhase) { _, phase in
            guard phase == .active else { return }
            windowState.markSelectedThreadViewedIfNeeded(in: appModel)
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
                titleText(
                    project: projectName(for: ref.connectionID, projectID: thread.projectId),
                    name: thread.displayName
                )
            }
        case .terminal(let ref):
            if let terminal = appModel.terminal(for: ref) {
                titleText(
                    project: projectName(for: ref.connectionID, projectID: terminal.projectId),
                    name: terminal.displayName
                )
            }
        }
    }

    private func titleText(project: String, name: String) -> some View {
        Text("\(Text("\(project):").fontWeight(.semibold)) \(name)")
            .fontWeight(.regular)
            .font(.headline)
            .lineLimit(1)
            .truncationMode(.tail)
    }

    @ViewBuilder
    private var exceptionalStatus: some View {
        HStack(spacing: Spacing.md) {
            if let ref = selectedThread,
                appModel.thread(for: ref)?.activityState == .needsInput
            {
                Label(
                    ThreadActivityState.needsInput.detailLabel,
                    systemImage: "exclamationmark.bubble.fill"
                )
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
            let terminal = thread.linkedTerminalId.flatMap { id in
                appModel.terminal(for: TerminalRef(connectionID: ref.connectionID, terminalID: id))
            }
            ThreadInspectorView(
                thread: thread,
                projectName: projectName(for: ref.connectionID, projectID: thread.projectId),
                sessionName: terminal?.isLive == true ? terminal?.sessionName : nil
            )
            .inspectorColumnWidth(min: 260, ideal: 320)
        }
    }

    private var selectedThread: ThreadRef? {
        windowState.selectedThread
    }

    private func viewModeBinding(_ ref: ThreadRef) -> Binding<ThreadViewMode> {
        Binding(
            get: { appModel.viewMode(for: ref) },
            set: { appModel.setViewMode($0, for: ref) }
        )
    }

    private func projectName(for connectionID: UUID, projectID: String) -> String {
        appModel.runtime(id: connectionID)?.projects.project(id: projectID)?.name
            ?? "Unknown Project"
    }
}

// Previews are compiled into Release builds too; the fixtures they use
// are not.
#if DEBUG

    #Preview {
        let appModel = AppModel.preview()
        let windowState = WindowState()
        let configStore = ConfigurationStore()
        RootView()
            .environment(appModel)
            .environment(windowState)
            .environment(
                WindowKeyboardRouter.forWindow(
                    appModel: appModel,
                    windowState: windowState,
                    configStore: configStore
                )
            )
            .preferredColorScheme(.dark)
    }

#endif
