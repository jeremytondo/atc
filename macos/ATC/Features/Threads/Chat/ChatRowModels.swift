// The Chat list's render models (ATC-214). The reducer stays value-typed and
// testable; these reference boxes are what the row views observe, so a text
// delta invalidates exactly one row instead of the whole pane. A box also
// owns the per-row view state that must survive LazyVStack recycling —
// disclosure expansion — and memoizes pretty-printed JSON per item version.
// `ChatRowBuilder` reuses boxes by id, so identity (and expansion) survives
// every structural rebuild.

import ATCAppServerAPI
import Observation
import OpenAPIRuntime

@Observable
final class ChatItemModel: Identifiable {
    private(set) var item: ThreadItem
    /// Tool-row / Thinking disclosure, here so scrolling cannot reset it.
    var isExpanded = false
    /// Pretty-printed JSON per item version, keyed by field name; cleared
    /// when the item changes. Not observed: it is a render-time memo.
    @ObservationIgnored private var prettyCache: [String: String] = [:]

    init(item: ThreadItem) {
        self.item = item
    }

    var id: String { item.id }

    func update(_ item: ThreadItem) {
        guard self.item != item else { return }
        self.item = item
        prettyCache = [:]
    }

    /// The pretty JSON of one of this item's fields ("input", "output",
    /// "arguments", "result"), computed once per item version.
    func pretty(_ field: String, _ value: OpenAPIValueContainer?) -> String {
        if let cached = prettyCache[field] { return cached }
        let rendered = PrettyJSON.string(value)
        prettyCache[field] = rendered
        return rendered
    }
}

/// One rendered node: the observed box plus its nested children.
struct ChatNodeModel: Identifiable {
    let box: ChatItemModel
    let children: [ChatNodeModel]

    var id: String { box.id }
}

enum ChatRowModel: Identifiable {
    case item(ChatNodeModel)
    case turnEnded(ThreadTurn)
    case pending(PendingPrompt)

    /// Mirrors `ChatRow.id`, so list identity is stable across rebuilds.
    var id: String {
        switch self {
        case .item(let node): "item:\(node.id)"
        case .turnEnded(let turn): "turn:\(turn.id)"
        case .pending(let prompt): "pending:\(prompt.id)"
        }
    }
}

enum ChatRowBuilder {
    /// The render rows for the reducer's pure rows, reusing existing boxes
    /// by id. Boxes for items no longer present are dropped.
    static func rows(
        from transcript: ChatTranscript,
        reusing boxes: inout [String: ChatItemModel]
    ) -> [ChatRowModel] {
        var used: Set<String> = []
        func makeNode(_ node: ChatNode) -> ChatNodeModel {
            let box = boxes[node.id] ?? ChatItemModel(item: node.item)
            box.update(node.item)
            boxes[node.id] = box
            used.insert(node.id)
            return ChatNodeModel(box: box, children: node.children.map(makeNode))
        }
        let rows = transcript.rows.map { row -> ChatRowModel in
            switch row {
            case .item(let node): .item(makeNode(node))
            case .turnEnded(let turn): .turnEnded(turn)
            case .pending(let prompt): .pending(prompt)
            }
        }
        boxes = boxes.filter { used.contains($0.key) }
        return rows
    }
}
