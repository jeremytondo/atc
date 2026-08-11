import SwiftUI

@main
struct ATCApp: App {
    @State private var appModel = AppModel()
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
            RootView(configStore: configStore)
        }
        .environment(windowState)
        .focusedSceneValue(windowState)
        .task {
            configStore.loadAtLaunchIfNeeded()
            appModel.registerWindow(windowState)
            await appModel.start()
        }
        .onDisappear {
            appModel.unregisterWindow(windowState)
        }
    }
}
