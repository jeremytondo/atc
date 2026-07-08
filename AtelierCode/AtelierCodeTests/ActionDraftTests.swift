import Foundation
import Testing
import CockpitAPI
@testable import AtelierCode

/// ActionDraft seeding, validation, and write-request building — the layer
/// that must round-trip every field because PUT is a full replace.
@MainActor
@Suite("ActionDraft")
struct ActionDraftTests {
    private func fullAction() -> CockpitAction {
        CockpitAction(
            name: "fancy",
            origin: "custom",
            enabled: false,
            label: "Fancy",
            description: "Does things",
            command: "fancy",
            args: ["--yes", "--color always"],
            prompt: .init(flag: "--prompt"),
            params: [
                "model": .init(type: "enum", values: ["fast", "smart"], default: .string("fast"), flag: "--model", label: "Model"),
                "verbose": .init(type: "bool", default: .bool(true), flag: "--verbose"),
            ]
        )
    }

    @Test("seed → writeRequest round-trips every field")
    func roundTrip() {
        let draft = ActionDraft(action: fullAction())
        #expect(draft.argsText == "--yes\n--color always")
        #expect(draft.acceptsPrompt)
        #expect(draft.promptFlag == "--prompt")
        #expect(!draft.enabled)
        #expect(draft.params.count == 2)

        let request = draft.writeRequest(routeName: "fancy")
        #expect(request.name == "fancy")
        #expect(request.label == "Fancy")
        #expect(request.description == "Does things")
        #expect(request.command == "fancy")
        #expect(request.args == ["--yes", "--color always"])
        #expect(request.prompt?.flag == "--prompt")
        #expect(request.enabled == false)
        let model = request.params?["model"]
        #expect(model?.type == "enum")
        #expect(model?.values == ["fast", "smart"])
        #expect(model?.default == .string("fast"))
        let verbose = request.params?["verbose"]
        #expect(verbose?.type == "bool")
        #expect(verbose?.default == .bool(true))
    }

    @Test("prompt toggle off omits the prompt spec entirely")
    func promptOmitted() {
        let draft = ActionDraft(action: fullAction())
        draft.acceptsPrompt = false
        #expect(draft.writeRequest(routeName: "fancy").prompt == nil)
        // Positional prompt: toggle on with empty flag still sends a spec.
        draft.acceptsPrompt = true
        draft.promptFlag = "  "
        let request = draft.writeRequest(routeName: "fancy")
        #expect(request.prompt != nil)
        #expect(request.prompt?.flag == nil)
    }

    @Test("create: explicit name wins, empty name is omitted for server derivation")
    func createNameHandling() {
        let draft = ActionDraft()
        draft.label = "My Cool Action"
        draft.command = "cool"
        #expect(draft.derivedName == "my-cool-action")
        #expect(draft.writeRequest(routeName: nil).name == nil)

        draft.name = "cool2"
        #expect(draft.derivedName == "cool2")
        #expect(draft.writeRequest(routeName: nil).name == "cool2")
    }

    @Test("args parse one per line, blank lines dropped, spaces within an arg kept")
    func argsParsing() {
        let draft = ActionDraft()
        draft.command = "x"
        draft.argsText = " --flag \n\n--path with space \n"
        #expect(draft.args == ["--flag", "--path with space"])
        #expect(draft.writeRequest(routeName: nil).args == ["--flag", "--path with space"])
    }

    @Test("validation catches the server's rejection cases")
    func validation() {
        let draft = ActionDraft()
        #expect(draft.validationMessage(isNew: true) != nil) // no command

        draft.command = "tool"
        #expect(draft.validationMessage(isNew: true) != nil) // no name/label
        draft.label = "!!!"
        #expect(draft.validationMessage(isNew: true) != nil) // slug comes up empty
        draft.name = "bad/name"
        #expect(draft.validationMessage(isNew: true) != nil) // invalid characters
        draft.name = "tool"
        #expect(draft.validationMessage(isNew: true) == nil)

        // Param rules.
        let param = ParamDraft()
        draft.params = [param]
        #expect(draft.validationMessage(isNew: true) != nil) // unnamed param
        param.name = "mode"
        #expect(draft.validationMessage(isNew: true) != nil) // enum without values
        param.valuesText = "a, b"
        #expect(draft.validationMessage(isNew: true) == nil)
        param.defaultValue = "c"
        #expect(draft.validationMessage(isNew: true) != nil) // default not in values
        param.defaultValue = "a"
        #expect(draft.validationMessage(isNew: true) == nil)

        // Bool defaulting to on requires a flag (server rule).
        let toggle = ParamDraft()
        toggle.name = "verbose"
        toggle.isEnum = false
        toggle.boolDefault = true
        draft.params.append(toggle)
        #expect(draft.validationMessage(isNew: true) != nil)
        toggle.flag = "--verbose"
        #expect(draft.validationMessage(isNew: true) == nil)

        // Duplicate param names.
        let dupe = ParamDraft()
        dupe.name = "mode"
        dupe.isEnum = false
        draft.params.append(dupe)
        #expect(draft.validationMessage(isNew: true) != nil)
    }

    @Test("existing actions skip create-only name rules")
    func existingSkipsNameRules() {
        let draft = ActionDraft()
        draft.command = "tool"
        // No name/label at all — fine on update, the route carries the name.
        #expect(draft.validationMessage(isNew: false) == nil)
    }

    @Test("param values parse comma-separated with trimming and dedupe for the picker")
    func paramValues() {
        let param = ParamDraft()
        param.valuesText = " fast , smart ,, fast "
        #expect(param.values == ["fast", "smart", "fast"])
        #expect(param.uniqueValues == ["fast", "smart"])
    }

    @Test("bool param seeded from a string default reads server back-compat values")
    func boolStringDefault() {
        let spec = CockpitAction.ParamSpec(type: "bool", default: .string("true"), flag: "--x")
        let param = ParamDraft(name: "x", spec: spec)
        #expect(param.boolDefault)
        // Normalizes to a native bool on the way back out.
        #expect(param.spec.default == .bool(true))
    }
}
