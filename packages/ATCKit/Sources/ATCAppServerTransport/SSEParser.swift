// SSEParser: incremental parser for the App Server's text/event-stream
// framing.
//
// The server emits two things: comment lines (`: connected` on open,
// `: heartbeat` every 25 s) and single-`data:`-line events. The parser still
// implements the general SSE rules that matter for correctness — bytes are
// buffered until a complete line, `\r\n` is tolerated, multiple `data:`
// lines join with `\n`, events dispatch on a blank line, and unknown fields
// are ignored — so chunk boundaries and framing variations can never corrupt
// an event.

import Foundation

struct SSEParser {
    enum Item: Equatable, Sendable {
        /// A comment line, leading colon stripped (`: connected` → "connected").
        case comment(String)
        /// A dispatched event's joined data payload.
        case data(String)
    }

    private var lineBuffer: [UInt8] = []
    private var pendingData: [String] = []

    /// Feed a chunk; returns every item completed by it. Comments surface as
    /// soon as their line completes — the connected/heartbeat signals must
    /// not wait for an event boundary.
    mutating func consume(_ bytes: some Sequence<UInt8>) -> [Item] {
        var items: [Item] = []
        for byte in bytes {
            guard byte == 0x0A else {
                lineBuffer.append(byte)
                continue
            }
            if lineBuffer.last == 0x0D {
                lineBuffer.removeLast()
            }
            let line = String(decoding: lineBuffer, as: UTF8.self)
            lineBuffer.removeAll(keepingCapacity: true)
            if let item = consumeLine(line) {
                items.append(item)
            }
        }
        return items
    }

    private mutating func consumeLine(_ line: String) -> Item? {
        if line.isEmpty {
            guard !pendingData.isEmpty else { return nil }
            let data = pendingData.joined(separator: "\n")
            pendingData.removeAll()
            return .data(data)
        }
        if line.hasPrefix(":") {
            return .comment(stripFieldSpace(line.dropFirst()))
        }
        let field: Substring
        let value: Substring
        if let colon = line.firstIndex(of: ":") {
            field = line[..<colon]
            value = line[line.index(after: colon)...]
        } else {
            field = line[...]
            value = ""
        }
        if field == "data" {
            pendingData.append(stripFieldSpace(value))
        }
        // id/event/retry and unknown fields are ignored: the contract's
        // stream is data-only with no replay.
        return nil
    }

    /// The SSE spec strips a single leading space after the colon.
    private func stripFieldSpace(_ value: Substring) -> String {
        String(value.hasPrefix(" ") ? value.dropFirst() : value)
    }
}
