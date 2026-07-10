import Foundation
import Testing
@testable import ATCAPI

/// Fixture captured from the live server on 2026-07-03 (`GET /api/actions`),
/// extended with a synthetic enum/bool param action matching the server's
/// closed ParamSpec schema.
private let actionsFixture = Data(#"""
{"actions":[
  {"name":"claude","origin":"modified","enabled":true,"label":"Claude","description":"Claude Code CLI","prompt":{},"params":{}},
  {"name":"codex","origin":"builtin","enabled":true,"label":"Codex","description":"OpenAI Codex CLI","prompt":{"flag":"--prompt"},"params":{}},
  {"name":"lazygit","origin":"custom","enabled":true,"label":"LazyGit","description":"Open LazyGit","params":{}},
  {"name":"fancy","origin":"custom","enabled":false,"params":{
    "model":{"type":"enum","values":["fast","smart"],"default":"fast","flag":"--model","label":"Model"},
    "verbose":{"type":"bool","flag":"--verbose","description":"Log more"}
  }}
]}
"""#.utf8)

private let environmentsFixture = Data(#"""
{"environments":[{"name":"host-login-shell","kind":"host-login-shell","label":"Host login shell","description":"Run through the host user's login-interactive shell","default":true}]}
"""#.utf8)

@Suite("Action & environment decoding")
struct ActionDecodingTests {
    @Test("actions decode; prompt presence drives acceptsPrompt")
    func actions() throws {
        let envelope = try JSONDecoder.atc().decode(ActionsEnvelope.self, from: actionsFixture)
        #expect(envelope.actions.count == 4)

        let claude = envelope.actions[0]
        #expect(claude.acceptsPrompt)
        #expect(claude.prompt?.flag == nil)
        #expect(claude.displayLabel == "Claude")

        let codex = envelope.actions[1]
        #expect(codex.prompt?.flag == "--prompt")

        let lazygit = envelope.actions[2]
        #expect(!lazygit.acceptsPrompt)

        let fancy = envelope.actions[3]
        #expect(!fancy.enabled)
        #expect(fancy.displayLabel == "fancy")
        let model = try #require(fancy.params["model"])
        #expect(model.isEnum)
        #expect(model.values == ["fast", "smart"])
        #expect(model.default == .string("fast"))
        let verbose = try #require(fancy.params["verbose"])
        #expect(verbose.isBool)
    }

    @Test("environments decode with default flag")
    func environments() throws {
        let envelope = try JSONDecoder.atc().decode(EnvironmentsEnvelope.self, from: environmentsFixture)
        let env = try #require(envelope.environments.first)
        #expect(env.isDefault)
        #expect(env.displayLabel == "Host login shell")
    }

    @Test("detail response decodes command/args; list entries leave them nil")
    func actionDetail() throws {
        // Captured from the live server on 2026-07-08 (`GET /api/actions/claude`).
        let detailFixture = Data(#"""
        {"name":"claude","origin":"modified","enabled":true,"label":"Claude","description":"Claude Code CLI","command":"claude","args":["--dangerously-skip-permissions"],"prompt":{},"params":{}}
        """#.utf8)
        let detail = try JSONDecoder.atc().decode(ATCAction.self, from: detailFixture)
        #expect(detail.command == "claude")
        #expect(detail.args == ["--dangerously-skip-permissions"])
        #expect(detail.isModified)

        let listEntry = try JSONDecoder.atc()
            .decode(ActionsEnvelope.self, from: actionsFixture).actions[0]
        #expect(listEntry.command == nil)
        #expect(listEntry.args == nil)
    }
}

@Suite("Action write request encoding")
struct ActionWriteRequestTests {
    private func encodeToObject(_ request: ActionWriteRequest) throws -> [String: Any] {
        let data = try JSONEncoder().encode(request)
        return try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    @Test("nil optionals are omitted, not null — the server treats absent prompt as no-prompt")
    func omitsNilFields() throws {
        let object = try encodeToObject(ActionWriteRequest(name: "lazygit", command: "lazygit"))
        #expect(object["name"] as? String == "lazygit")
        #expect(object["command"] as? String == "lazygit")
        #expect(!object.keys.contains("prompt"))
        #expect(!object.keys.contains("enabled"))
        #expect(!object.keys.contains("params"))
        #expect(!object.keys.contains("args"))
    }

    @Test("full request round-trips prompt, params, and enabled")
    func encodesFullRequest() throws {
        let request = ActionWriteRequest(
            name: "fancy",
            label: "Fancy",
            description: "Does things",
            command: "fancy",
            args: ["--yes"],
            prompt: .init(flag: "--prompt"),
            params: [
                "model": .init(type: "enum", values: ["fast", "smart"], default: .string("fast"), flag: "--model"),
                "verbose": .init(type: "bool", flag: "--verbose"),
            ],
            enabled: false
        )
        let object = try encodeToObject(request)
        #expect(object["enabled"] as? Bool == false)
        #expect(object["args"] as? [String] == ["--yes"])
        let prompt = try #require(object["prompt"] as? [String: Any])
        #expect(prompt["flag"] as? String == "--prompt")
        let params = try #require(object["params"] as? [String: Any])
        let model = try #require(params["model"] as? [String: Any])
        #expect(model["type"] as? String == "enum")
        #expect(model["values"] as? [String] == ["fast", "smart"])
        #expect(model["default"] as? String == "fast")

        // A positional prompt encodes as an (empty-ish) object, never dropped.
        let positional = try encodeToObject(ActionWriteRequest(command: "x", prompt: .init()))
        #expect(positional["prompt"] is [String: Any])
    }
}

@Suite("Action name rules")
struct ActionNameTests {
    @Test("validity mirrors the server's ^[A-Za-z0-9_-]+$")
    func validity() {
        #expect(ActionName.isValid("lazygit"))
        #expect(ActionName.isValid("My_Action-2"))
        #expect(!ActionName.isValid(""))
        #expect(!ActionName.isValid("bad/name"))
        #expect(!ActionName.isValid("has space"))
        #expect(!ActionName.isValid("émoji"))
    }

    @Test("slugify mirrors the server: lowercase, collapse to dashes, trim")
    func slugify() {
        #expect(ActionName.slugify("LazyGit") == "lazygit")
        #expect(ActionName.slugify("My Cool Action") == "my-cool-action")
        #expect(ActionName.slugify("  spaced -- out  ") == "spaced-out")
        #expect(ActionName.slugify("!!!") == "")
        #expect(ActionName.slugify("v2.0 (beta)") == "v2-0-beta")
    }
}
