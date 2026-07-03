import Foundation
import Testing
@testable import CockpitAPI

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
        let envelope = try JSONDecoder.cockpit().decode(ActionsEnvelope.self, from: actionsFixture)
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
        let envelope = try JSONDecoder.cockpit().decode(EnvironmentsEnvelope.self, from: environmentsFixture)
        let env = try #require(envelope.environments.first)
        #expect(env.isDefault)
        #expect(env.displayLabel == "Host login shell")
    }
}
