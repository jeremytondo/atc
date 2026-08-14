import Foundation
import Testing

@testable import ATCAppServerTransport

@Suite("SSE parser")
struct SSEParserTests {
    private func consume(_ text: String, chunkSize: Int = .max) -> [SSEParser.Item] {
        var parser = SSEParser()
        var items: [SSEParser.Item] = []
        let bytes = Array(text.utf8)
        var index = 0
        while index < bytes.count {
            let end = min(index + chunkSize, bytes.count)
            items.append(contentsOf: parser.consume(bytes[index..<end]))
            index = end
        }
        return items
    }

    @Test("the server's opening bytes parse as the connected comment")
    func openingComment() {
        #expect(consume(": connected\n\n") == [.comment("connected")])
    }

    @Test("data events dispatch on the blank line")
    func dataEvent() {
        let wire = ": connected\n\ndata: {\"resource\":\"thread\"}\n\n"
        #expect(
            consume(wire) == [
                .comment("connected"),
                .data("{\"resource\":\"thread\"}"),
            ]
        )
    }

    @Test("byte-at-a-time chunking changes nothing")
    func chunkSplits() {
        let wire = ": connected\n\ndata: {\"id\":\"x\"}\n\n: heartbeat\n\n"
        let whole = consume(wire)
        #expect(whole == consume(wire, chunkSize: 1))
        #expect(whole == consume(wire, chunkSize: 3))
        #expect(whole == [.comment("connected"), .data("{\"id\":\"x\"}"), .comment("heartbeat")])
    }

    @Test("CRLF line endings are tolerated")
    func crlf() {
        #expect(
            consume(": connected\r\n\r\ndata: 1\r\n\r\n")
                == [.comment("connected"), .data("1")]
        )
    }

    @Test("multiple data lines join with a newline")
    func multiLineData() {
        #expect(consume("data: a\ndata: b\n\n") == [.data("a\nb")])
    }

    @Test("only the first space after the colon is stripped")
    func fieldSpaceStripping() {
        #expect(consume("data:  padded\n\n") == [.data(" padded")])
        #expect(consume("data:tight\n\n") == [.data("tight")])
        #expect(consume(":comment-no-space\n\n") == [.comment("comment-no-space")])
    }

    @Test("unknown fields and fieldless lines are ignored")
    func unknownFields() {
        #expect(consume("id: 7\nevent: change\nretry: 100\nnonsense\ndata: x\n\n") == [.data("x")])
    }

    @Test("blank lines without pending data dispatch nothing")
    func emptyDispatch() {
        #expect(consume("\n\n\n") == [])
    }

    @Test("an incomplete line stays buffered until its newline arrives")
    func incompleteLine() {
        var parser = SSEParser()
        #expect(parser.consume(Array("data: par".utf8)) == [])
        #expect(parser.consume(Array("tial\n".utf8)) == [])
        #expect(parser.consume(Array("\n".utf8)) == [.data("partial")])
    }
}
