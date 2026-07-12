import SwiftUI
import ATCAPI

/// Thin per-window router: the Workspace shell (mounted once a Workspace
/// has been opened, then kept mounted for the window's life) underneath an
/// opaque Dashboard cover. This generalizes the `TerminalPane` cover
/// pattern — returning from the Dashboard never tears down or replays a
/// terminal surface.
struct RootView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    var body: some View {
        @Bindable var windowState = windowState
        ZStack {
            if windowState.hasOpenedWorkspaceShell {
                WorkspaceShellView()
            }
            if windowState.route == .dashboard {
                DashboardView(
                    onOpenWorkspace: { windowState.openWorkspace($0, in: appModel) },
                    onCreateWorkspace: { ref in
                        windowState.createWorkspaceContext = CreateWorkspaceContext(mode: .fixed(ref))
                    },
                    onCreateProject: { windowState.isCreateProjectPresented = true }
                )
                .background()
            }
        }
        .sheet(isPresented: $windowState.isCreateProjectPresented) {
            CreateProjectSheet()
        }
        .sheet(item: $windowState.createWorkspaceContext) { context in
            CreateWorkspaceSheet(context: context) { ref in
                // Creation opens the new Workspace, not just a selection.
                windowState.openWorkspace(ref, in: appModel)
            }
        }
        .sheet(item: $windowState.startSessionKind) { kind in
            if let ref = appModel.openWorkspace {
                StartWorkspaceSessionSheet(kind: kind, workspaceRef: ref) { newRef in
                    appModel.selection = newRef
                }
            }
        }
        .onChange(of: appModel.openWorkspaceExists) { _, exists in
            // Deleted via web/CLI, or its Connection removed: back to the
            // Dashboard. Session terminations that accompany a remote
            // delete flow through the existing attach-end machinery.
            if !exists, appModel.openWorkspace != nil {
                windowState.handleOpenWorkspaceGone(in: appModel)
            }
        }
    }
}

#Preview {
    RootView()
        .environment(AppModel.preview())
        .environment(WindowState())
        .preferredColorScheme(.dark)
}
