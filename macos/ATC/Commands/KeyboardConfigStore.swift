import Foundation
import Observation
import OSLog

struct ConfigNotice: Equatable {
    let message: String
}

@MainActor
@Observable
final class KeyboardConfigStore {
    private(set) var keymap: ResolvedKeymap
    private(set) var notice: ConfigNotice?
    private(set) var diagnostics: [ConfigDiagnostic] = []

    @ObservationIgnored private let configURL: URL
    @ObservationIgnored private let fileManager: FileManager
    @ObservationIgnored private let logger = Logger(
        subsystem: "ElevenIdeas.atc",
        category: "keyboard"
    )

    init(
        configURL: URL = KeyboardConfigStore.defaultConfigURL(),
        fileManager: FileManager = .default
    ) {
        self.configURL = configURL
        self.fileManager = fileManager
        switch Keymap.resolve(generation: 0) {
        case .success(let keymap): self.keymap = keymap
        case .failure: preconditionFailure("Compiled keyboard defaults must be valid")
        }
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
            .appending(path: "config.toml", directoryHint: .notDirectory)
    }

    private func load(isLaunch: Bool) {
        let nextGeneration = keymap.generation + 1
        guard fileManager.fileExists(atPath: configURL.path) else {
            apply(parsed: .empty, generation: nextGeneration, isLaunch: isLaunch)
            return
        }

        let parsed: ParsedConfig
        do {
            parsed = KeyboardConfigParser.parse(data: try Data(contentsOf: configURL))
        } catch {
            fail(
                diagnostics: [.init(
                    severity: .error,
                    line: nil,
                    message: "Could not read config.toml: \(error.localizedDescription)"
                )],
                isLaunch: isLaunch
            )
            return
        }
        apply(parsed: parsed, generation: nextGeneration, isLaunch: isLaunch)
    }

    private func apply(parsed: ParsedConfig, generation: Int, isLaunch: Bool) {
        switch Keymap.resolve(user: parsed, generation: generation) {
        case .success(let candidate):
            keymap = candidate
            diagnostics = parsed.diagnostics
            notice = nil
            log(parsed.diagnostics)
        case .failure(let failure):
            fail(diagnostics: failure.diagnostics, isLaunch: isLaunch)
        }
    }

    private func fail(diagnostics: [ConfigDiagnostic], isLaunch: Bool) {
        self.diagnostics = diagnostics
        log(diagnostics)
        let errorCount = diagnostics.count { $0.severity == .error }
        let disposition = isLaunch
            ? "using default keybindings"
            : "keeping the previous keybindings"
        notice = ConfigNotice(
            message: "Keyboard configuration was not loaded (\(errorCount) \(errorCount == 1 ? "error" : "errors")) — \(disposition). See log for details."
        )
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
}
