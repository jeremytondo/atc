// Consumes the shared cross-language vectors in
// packages/attach-protocol/fixtures/ so protocol drift between this client
// and the server fails a test instead of a terminal window (the TypeScript
// side consumes the same files in attachProtocolFixtures.test.ts).

import Foundation
import Testing
@testable import ATCAppServerTransport

private func fixture<T: Decodable>(_ name: String, as type: T.Type) throws -> T {
    // Tests run from the repo checkout, so the shared fixtures resolve
    // relative to this source file; there is no bundle-resource copy to
    // drift from the canonical files.
    let url = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent()
        .deletingLastPathComponent()
        .deletingLastPathComponent()
        .deletingLastPathComponent()
        .appending(path: "attach-protocol/fixtures/\(name).json")
    return try JSONDecoder().decode(T.self, from: Data(contentsOf: url))
}

@Suite("Attach protocol fixtures")
struct AttachProtocolFixtureTests {
    struct URLFixture: Decodable {
        struct Case: Decodable {
            let name: String
            let base: String
            let terminalId: String
            let cols: Int?
            let rows: Int?
            let expect: String
        }

        let cases: [Case]
    }

    @Test("attach URLs match the shared construction vectors")
    func attachURLs() throws {
        let fixture = try fixture("attach-urls", as: URLFixture.self)
        for testCase in fixture.cases {
            let size: (cols: Int, rows: Int)? =
                if let cols = testCase.cols, let rows = testCase.rows {
                    (cols, rows)
                } else {
                    nil
                }
            let url = AttachProtocol.attachURL(
                base: URL(string: testCase.base)!,
                terminalID: testCase.terminalId,
                size: size
            )
            #expect(url.absoluteString == testCase.expect, "\(testCase.name)")
        }
    }

    struct ControlFrameFixture: Decodable {
        struct Valid: Decodable {
            struct Frame: Decodable {
                let type: String
                let cols: Int?
                let rows: Int?
            }

            let name: String
            let wire: String
            let frame: Frame
        }

        struct Ignored: Decodable {
            let name: String
            let wire: String
        }

        let valid: [Valid]
        let ignored: [Ignored]
    }

    @Test("control frames decode, and their encodings round-trip")
    func controlFrames() throws {
        let fixture = try fixture("control-frames", as: ControlFrameFixture.self)
        for entry in fixture.valid {
            let expected: AttachProtocol.ControlFrame? =
                switch entry.frame.type {
                case "resize": .resize(cols: entry.frame.cols!, rows: entry.frame.rows!)
                case "ping": .ping
                case "pong": .pong
                default: nil
                }
            let frame = try #require(expected, "unknown fixture frame type: \(entry.frame.type)")
            #expect(AttachProtocol.ControlFrame.decode(entry.wire) == frame, "\(entry.name)")
            #expect(AttachProtocol.ControlFrame.decode(frame.wire) == frame, "\(entry.name) round-trip")
        }
        for entry in fixture.ignored {
            #expect(AttachProtocol.ControlFrame.decode(entry.wire) == nil, "\(entry.name)")
        }
    }

    struct ClampFixture: Decodable {
        enum Raw: Decodable {
            case string(String)
            case number(Double)
            case absent

            init(from decoder: any Decoder) throws {
                let container = try decoder.singleValueContainer()
                if container.decodeNil() {
                    self = .absent
                } else if let number = try? container.decode(Double.self) {
                    self = .number(number)
                } else {
                    self = .string(try container.decode(String.self))
                }
            }
        }

        struct Case: Decodable {
            let name: String
            let raw: Raw
            let expect: Int
        }

        let fallback: Int
        let cases: [Case]
    }

    @Test("dimension clamping matches the shared vectors")
    func dimensionClamp() throws {
        let fixture = try fixture("dimension-clamp", as: ClampFixture.self)
        for testCase in fixture.cases {
            let clamped =
                switch testCase.raw {
                case .string(let raw):
                    AttachProtocol.clampDimension(raw, fallback: fixture.fallback)
                case .number(let raw):
                    AttachProtocol.clampDimension(raw, fallback: fixture.fallback)
                case .absent:
                    AttachProtocol.clampDimension(nil as Double?, fallback: fixture.fallback)
                }
            #expect(clamped == testCase.expect, "\(testCase.name)")
        }
    }

    struct CloseFixture: Decodable {
        struct Close: Decodable {
            let code: Int
            let reason: String?
            let sender: String
            let retryable: Bool?
        }

        let closes: [Close]
    }

    @Test("close classification matches the shared vocabulary")
    func closeCodes() throws {
        let fixture = try fixture("close-codes", as: CloseFixture.self)
        for close in fixture.closes {
            let disposition = AttachProtocol.classifyClose(code: close.code, reason: close.reason)
            // The fixture encodes the meaning through `retryable`: false is
            // the one authoritative death, null is the client's own detach,
            // true must land in the reattach path.
            switch close.retryable {
            case false:
                #expect(disposition == .terminalEnded, "\(close.reason ?? "\(close.code)")")
            case nil:
                #expect(disposition == .detach, "\(close.reason ?? "\(close.code)")")
            case true:
                #expect(disposition == .retryable, "\(close.reason ?? "\(close.code)")")
            }
        }
    }
}

@Suite("Attach end-reason mapping")
struct AttachEndReasonTests {
    @Test("pre-upgrade rejections map by status")
    func preUpgradeStatuses() {
        #expect(
            AttachSocket.endReason(
                statusCode: 410, closeCode: .invalid, closeReason: nil, errorDescription: "gone"
            ) == .terminalEnded
        )
        #expect(
            AttachSocket.endReason(
                statusCode: 404, closeCode: .invalid, closeReason: nil, errorDescription: "missing"
            ) == .rejected(statusCode: 404)
        )
        #expect(
            AttachSocket.endReason(
                statusCode: 426, closeCode: .invalid, closeReason: nil, errorDescription: "upgrade"
            ) == .rejected(statusCode: 426)
        )
    }

    @Test("server closes map through the shared vocabulary")
    func serverCloses() {
        #expect(
            AttachSocket.endReason(
                closeCode: .normalClosure,
                closeReason: Data("terminal_ended".utf8),
                errorDescription: "closed"
            ) == .terminalEnded
        )
        #expect(
            AttachSocket.endReason(
                closeCode: .internalServerError,
                closeReason: Data("ping_timeout".utf8),
                errorDescription: "closed"
            ) == .retryable("ping_timeout")
        )
        #expect(
            AttachSocket.endReason(
                closeCode: .abnormalClosure,
                closeReason: nil,
                errorDescription: "socket closed"
            ) == .retryable("socket closed")
        )
    }

    @Test("a client-initiated close wins over any other signal")
    func clientClose() {
        #expect(
            AttachSocket.endReason(
                closedByClient: true,
                closeCode: .normalClosure,
                closeReason: Data("terminal_ended".utf8),
                errorDescription: "closed"
            ) == .closedByClient
        )
    }

    @Test("a successful upgrade's 101 does not shadow the close reason")
    func upgradeStatusIgnored() {
        #expect(
            AttachSocket.endReason(
                statusCode: 101,
                closeCode: .internalServerError,
                closeReason: Data("attach_failed".utf8),
                errorDescription: "closed"
            ) == .retryable("attach_failed")
        )
    }
}
