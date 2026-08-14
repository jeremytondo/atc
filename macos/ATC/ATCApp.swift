import SwiftUI

@main
struct ATCApp: App {
    // Only this call site gets the system notifier, so tests and previews can
    // never post to the real Notification Center (see `ThreadNotifier`).
    @State private var appModel = AppModel(threadNotifier: .system())
    @State private var configurationStore = ConfigurationStore { preferences in
        TerminalSessionController.applyTerminalPreferences(preferences)
    }

    var body: some Scene {
        WindowGroup {
            WindowRootView(appModel: appModel, configStore: configurationStore)
                .environment(appModel)
                .environment(configurationStore)
                .preferredColorScheme(.dark)
        }
        .commands {
            AppCommands(appModel: appModel, configStore: configurationStore)
        }
        Settings {
            SettingsView()
                .environment(appModel)
                .preferredColorScheme(.dark)
                // Settings can be reached before any window's task ran
                // (restoration edges); an unloaded Connection list must
                // never be observable — a save would persist over it.
                .task { appModel.start() }
        }
    }
}

/// The per-window scene root: owns this window's `WindowState` and keyboard
/// router (one of each per window — the seam multi-window builds on),
/// publishes the state for menu commands via the focused-scene value, and
/// registers it with the model for reconciliation. Launch I/O (config file,
/// Keychain hydration) runs in the awaited task below, off the first frame.
private struct WindowRootView: View {
    let appModel: AppModel
    let configStore: ConfigurationStore

    @State private var windowState: WindowState
    @State private var router: WindowKeyboardRouter
    /// `.key` exactly while this window is the key window; the model keeps
    /// its window list in key order so banner clicks open where the user
    /// last worked.
    @Environment(\.controlActiveState) private var controlActiveState

    init(appModel: AppModel, configStore: ConfigurationStore) {
        self.appModel = appModel
        self.configStore = configStore
        let windowState = WindowState()
        _windowState = State(initialValue: windowState)
        _router = State(initialValue: WindowKeyboardRouter.forWindow(
            appModel: appModel,
            windowState: windowState,
            configStore: configStore
        ))
    }

    var body: some View {
        KeyboardRoutingContainer(
            router: router,
            windowState: windowState,
            configStore: configStore
        ) {
            RootView()
        }
        .environment(windowState)
        .focusedSceneValue(windowState)
        .task {
            configStore.loadAtLaunchIfNeeded()
            appModel.registerWindow(windowState)
            appModel.start()
        }
        .onDisappear {
            appModel.unregisterWindow(windowState)
        }
        .onChange(of: controlActiveState) { _, state in
            guard state == .key else { return }
            appModel.noteWindowKeyed(windowState)
        }
    }
}
