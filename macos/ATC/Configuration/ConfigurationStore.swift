import Foundation
import Observation
import OSLog

struct ConfigNotice: Equatable {
    let message: String
}

@MainActor
@Observable
final class ConfigurationStore {
    private(set) var configuration: AppConfiguration
    private(set) var notice: ConfigNotice?
    private(set) var diagnostics: [ConfigDiagnostic] = []

    @ObservationIgnored let configURL: URL
    @ObservationIgnored private let fileManager: FileManager
    @ObservationIgnored private let onTerminalPreferencesApplied: ((TerminalPreferences) -> Void)?
    @ObservationIgnored private let logger = Logger(
        subsystem: "ElevenIdeas.atc",
        category: "configuration"
    )

    var configDirectoryURL: URL {
        configURL.deletingLastPathComponent()
    }

    init(
        configURL: URL = ConfigurationStore.defaultConfigURL(),
        fileManager: FileManager = .default,
        onTerminalPreferencesApplied: ((TerminalPreferences) -> Void)? = nil
    ) {
        self.configURL = configURL
        self.fileManager = fileManager
        self.onTerminalPreferencesApplied = onTerminalPreferencesApplied
        self.configuration = Self.defaultConfiguration(generation: 0)
    }

    func loadAtLaunch() {
        load(isLaunch: true)
    }

    func reload() {
        load(isLaunch: false)
    }

    func dismissNotice() {
        notice = nil
    }

    nonisolated static func defaultConfigURL(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        homeDirectory: URL = FileManager.default.homeDirectoryForCurrentUser
    ) -> URL {
        let base: URL
        if let xdg = environment["XDG_CONFIG_HOME"]?
            .trimmingCharacters(in: .whitespacesAndNewlines),
           !xdg.isEmpty {
            base = URL(fileURLWithPath: xdg, isDirectory: true)
        } else {
            base = homeDirectory.appending(path: ".config", directoryHint: .isDirectory)
        }
        return base
            .appending(path: "atc", directoryHint: .isDirectory)
            .appending(path: "macos.toml", directoryHint: .notDirectory)
    }

    private func load(isLaunch: Bool) {
        let nextGeneration = configuration.keymap.generation + 1
        guard fileManager.fileExists(atPath: configURL.path) else {
            configuration = Self.defaultConfiguration(generation: nextGeneration)
            diagnostics = []
            notice = nil
            onTerminalPreferencesApplied?(configuration.terminal)
            return
        }

        let parsed: ParsedConfig
        do {
            parsed = ConfigurationLoader.parse(data: try Data(contentsOf: configURL))
        } catch {
            fail(
                diagnostics: [.init(
                    severity: .error,
                    message: "Could not read macos.toml: \(error.localizedDescription)"
                )],
                isLaunch: isLaunch
            )
            return
        }

        switch Keymap.resolve(user: parsed, generation: nextGeneration) {
        case .success(let keymap):
            configuration = AppConfiguration(keymap: keymap, terminal: parsed.terminal)
            diagnostics = parsed.diagnostics
            notice = nil
            log(parsed.diagnostics)
            onTerminalPreferencesApplied?(configuration.terminal)
        case .failure(let failure):
            fail(diagnostics: failure.diagnostics, isLaunch: isLaunch)
        }
    }

    private func fail(diagnostics: [ConfigDiagnostic], isLaunch: Bool) {
        self.diagnostics = diagnostics
        log(diagnostics)
        let errors = diagnostics.filter { $0.severity == .error }
        let disposition = isLaunch
            ? "using defaults"
            : "keeping the previous configuration"
        let firstError = errors.first?.message ?? "unknown error"
        notice = ConfigNotice(
            message: "Configuration was not loaded — \(disposition). "
                + "First error: \(firstError) "
                + "(see log for \(errors.count) \(errors.count == 1 ? "error" : "errors"))."
        )
        if isLaunch {
            onTerminalPreferencesApplied?(configuration.terminal)
        }
    }

    private func log(_ diagnostics: [ConfigDiagnostic]) {
        for diagnostic in diagnostics {
            let message = "\(configURL.path): \(diagnostic.description)"
            switch diagnostic.severity {
            case .error: logger.error("\(message, privacy: .public)")
            case .warning: logger.warning("\(message, privacy: .public)")
            }
        }
    }

    private static func defaultConfiguration(generation: Int) -> AppConfiguration {
        switch Keymap.resolve(generation: generation) {
        case .success(let keymap): AppConfiguration(keymap: keymap)
        case .failure: preconditionFailure("Compiled application defaults must be valid")
        }
    }
}
