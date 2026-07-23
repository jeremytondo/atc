import Testing
import ATCAPI
@testable import ATC

@MainActor
@Suite("ActionDraft")
struct ActionDraftTests {
    private func fullAction() -> ATCAction {
        ATCAction(
            id: "act_fancy",
            name: "Fancy",
            description: "Does things",
            enabled: false,
            command: "fancy",
            args: ["--yes", "--color always"],
            isAgent: true
        )
    }

    @Test("seeding preserves every editable field")
    func seeding() {
        let draft = ActionDraft(action: fullAction())

        #expect(draft.name == "Fancy")
        #expect(draft.descriptionText == "Does things")
        #expect(draft.command == "fancy")
        #expect(draft.argsText == "--yes\n--color always")
        #expect(draft.isAgent)
        #expect(!draft.enabled)
    }

    @Test("create request carries the complete draft")
    func createRequest() {
        let request = ActionDraft(action: fullAction()).createRequest()

        #expect(request.name == "Fancy")
        #expect(request.description == "Does things")
        #expect(request.command == "fancy")
        #expect(request.args == ["--yes", "--color always"])
        #expect(request.isAgent == true)
        #expect(request.enabled == false)
    }

    @Test("patch clears an empty description and carries all mutable fields")
    func patchClearsDescription() {
        let draft = ActionDraft(action: fullAction())
        draft.descriptionText = "  "
        draft.name = "Renamed"

        let patch = draft.patch()

        #expect(patch.name == "Renamed")
        #expect(patch.description == nil)
        #expect(patch.clearDescription)
        #expect(patch.command == "fancy")
        #expect(patch.args == ["--yes", "--color always"])
        #expect(patch.isAgent == true)
        #expect(patch.enabled == false)
    }

    @Test("arguments are newline-separated literals")
    func argsParsing() {
        let draft = ActionDraft()
        draft.argsText = "--flag\n--path with space\n  padded  \n\n"

        #expect(draft.args == ["--flag", "--path with space", "  padded  "])
    }

    @Test("validation requires only name and command")
    func validation() {
        let draft = ActionDraft()
        #expect(draft.validationMessage() != nil)

        draft.name = "Any display name / punctuation!"
        #expect(draft.validationMessage() != nil)

        draft.command = "tool"
        #expect(draft.validationMessage() == nil)
    }
}
