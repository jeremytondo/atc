import Foundation
import Testing
@testable import ATCAPI

private let actionsFixture = Data(#"""
{"actions":[
  {"id":"act_vpj2tlg9viqd8ms52ptuvao5c4","name":"Claude","description":"Anthropic's coding agent","enabled":true,"command":"claude","args":[],"isAgent":true},
  {"id":"act_0123456789abcdefghijklmnop","name":"Dev server","enabled":false,"command":"npm","args":["run","dev"],"isAgent":false}
]}
"""#.utf8)

@Suite("Action decoding")
struct ActionDecodingTests {
    @Test("list entries decode as complete action definitions")
    func actions() throws {
        let envelope = try JSONDecoder.atc().decode(ActionsEnvelope.self, from: actionsFixture)
        #expect(envelope.actions.count == 2)

        let claude = envelope.actions[0]
        #expect(claude.id == "act_vpj2tlg9viqd8ms52ptuvao5c4")
        #expect(claude.name == "Claude")
        #expect(claude.description == "Anthropic's coding agent")
        #expect(claude.enabled)
        #expect(claude.command == "claude")
        #expect(claude.args.isEmpty)
        #expect(claude.isAgent)

        let devServer = envelope.actions[1]
        #expect(devServer.description == nil)
        #expect(!devServer.enabled)
        #expect(devServer.args == ["run", "dev"])
        #expect(!devServer.isAgent)
    }

    @Test("Identifiable uses the server-generated id")
    func identity() throws {
        let action = try JSONDecoder.atc().decode(ActionsEnvelope.self, from: actionsFixture).actions[0]
        #expect(action.id != action.name)
    }
}

@Suite("Action request encoding")
struct ActionRequestTests {
    private func encodeToObject(_ request: some Encodable) throws -> [String: Any] {
        let data = try JSONEncoder().encode(request)
        return try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    @Test("create omits optional fields so the server applies defaults")
    func createOmitsNilFields() throws {
        let object = try encodeToObject(ActionCreate(name: "Neovim", command: "nvim"))
        #expect(object["name"] as? String == "Neovim")
        #expect(object["command"] as? String == "nvim")
        #expect(!object.keys.contains("description"))
        #expect(!object.keys.contains("args"))
        #expect(!object.keys.contains("enabled"))
        #expect(!object.keys.contains("isAgent"))
    }

    @Test("create encodes every supplied field")
    func createEncodesFullRequest() throws {
        let object = try encodeToObject(ActionCreate(
            name: "Dev server",
            description: "Run the app",
            command: "npm",
            args: ["run", "dev"],
            enabled: false,
            isAgent: false
        ))
        #expect(object["description"] as? String == "Run the app")
        #expect(object["args"] as? [String] == ["run", "dev"])
        #expect(object["enabled"] as? Bool == false)
        #expect(object["isAgent"] as? Bool == false)
    }

    @Test("patch distinguishes omitted description from explicit null")
    func patchDescriptionOmittedVersusNull() throws {
        let omitted = try encodeToObject(ActionPatch(enabled: false))
        #expect(!omitted.keys.contains("description"))
        #expect(!omitted.keys.contains("clearDescription"))

        let cleared = try encodeToObject(ActionPatch(clearDescription: true))
        #expect(cleared.keys.contains("description"))
        #expect(cleared["description"] is NSNull)
        #expect(!cleared.keys.contains("clearDescription"))
    }

    @Test("patch encodes supplied fields and omits the rest")
    func patchEncodesPartialRequest() throws {
        let object = try encodeToObject(ActionPatch(
            name: "Editor",
            description: "Open files",
            args: [],
            isAgent: false
        ))
        #expect(object["name"] as? String == "Editor")
        #expect(object["description"] as? String == "Open files")
        #expect(object["args"] as? [String] == [])
        #expect(object["isAgent"] as? Bool == false)
        #expect(!object.keys.contains("command"))
        #expect(!object.keys.contains("enabled"))
    }
}
