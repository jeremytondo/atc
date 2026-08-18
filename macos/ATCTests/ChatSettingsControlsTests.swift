import ATCAppServerAPI
import Foundation
import Testing

@testable import ATC

/// The composer's menu entries (ATC-205): the model menu carries the
/// selected model's reasoning levels (none for a model without effort),
/// radios reflect the thread's settings, a missing catalog degrades to
/// loading / retry, and the narrow-row ⋯ menu holds Mode and Access.
@MainActor
@Suite("ChatSettingsControls")
struct ChatSettingsControlsTests {
    private let catalog = [
        Fixtures.model("big", displayName: "Big", isDefault: true, effortLevels: [.low, .high]),
        Fixtures.model("tiny", effortLevels: []),
    ]

    private func controls(model: String, models: [AgentModel]?, error: String? = nil) -> ChatSettingsControls {
        ChatSettingsControls(
            thread: Fixtures.thread(id: "t", settings: Fixtures.settings(model: model, reasoning: .high)),
            models: models, modelsError: error, update: { _ in }, reloadModels: {})
    }

    private func titles(_ entries: [PopupMenuEntry]) -> [String] {
        entries.map { entry in
            switch entry {
            case .header(let title): "#\(title)"
            case .separator: "-"
            case .item(let title, let isSelected, let isDestructive, _):
                "\(title)\(isSelected ? "*" : "")\(isDestructive ? "!" : "")"
            }
        }
    }

    @Test("the model menu radios the stored model and lists its reasoning levels; no effort, no section")
    func modelMenu() {
        #expect(
            titles(controls(model: "big", models: catalog).modelEntries) == [
                "#Model", "Big*", "tiny", "-", "#Reasoning", "Low", "High*",
            ])
        #expect(titles(controls(model: "tiny", models: catalog).modelEntries) == ["#Model", "Big", "tiny*"])
    }

    @Test("no catalog is loading; a failed read offers retry")
    func missingCatalog() {
        #expect(titles(controls(model: "big", models: nil).modelEntries) == ["#Loading models…"])
        #expect(
            titles(controls(model: "big", models: nil, error: "Claude is not installed").modelEntries)
                == ["#Claude is not installed", "Retry"])
    }

    @Test("mode and access radio the thread's settings; the ⋯ menu sections them; Full access is marked")
    func modeAndAccess() {
        let controls = controls(model: "big", models: catalog)
        #expect(titles(controls.modeEntries) == ["Chat*", "Plan"])
        #expect(titles(controls.accessEntries) == ["Supervised", "Auto-accept edits", "Auto*", "Full access!"])
        #expect(
            titles(controls.settingsEntries) == [
                "#Mode", "Chat*", "Plan", "-",
                "#Access", "Supervised", "Auto-accept edits", "Auto*", "Full access!",
            ])
    }
}
