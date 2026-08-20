// The transcript as render rows (ATC-214): what the Chat list shows, built
// by `ChatRowBuilder` from the reducer's copy. Items nest under their
// `parentItemId` (subagent / nested-tool work shows under the row that
// spawned it), a turn that failed or was interrupted gets one marker after
// its last item — the only visible turn boundary — and pending prompts (the
// optimistic echo) sit at the tail until the server shows them somewhere
// real.
//
// Rows hold `ChatItemModel` reference boxes, which is what the row views
// observe — a text delta invalidates exactly one row instead of the whole
// pane. A box also owns the per-row view state that must survive LazyVStack
// recycling — disclosure expansion — and memoizes pretty-printed JSON per
// item version. The builder reuses boxes by id, so identity (and expansion)
// survives every structural rebuild.

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
    /// A turn that ended abnormally, placed after its last item.
    case turnEnded(ThreadTurn)
    /// A prompt sent from this client that the transcript does not carry yet.
    case pending(PendingPrompt)

    var id: String {
        switch self {
        case .item(let node): "item:\(node.id)"
        case .turnEnded(let turn): "turn:\(turn.id)"
        case .pending(let prompt): "pending:\(prompt.id)"
        }
    }
}

enum ChatRowBuilder {
    /// The render rows for the copy as it stands, reusing existing boxes by
    /// id. Boxes for items no longer present are dropped.
    static func rows(
        from transcript: ChatTranscript,
        reusing boxes: inout [String: ChatItemModel]
    ) -> [ChatRowModel] {
        let ids = Set(transcript.items.map(\.id))
        var childItems: [String: [ThreadItem]] = [:]
        var topLevel: [ThreadItem] = []
        for item in transcript.items {
            // A parent that isn't on this page (older) leaves the child at
            // top level rather than dropping it.
            if let parent = item.parentItemId, ids.contains(parent) {
                childItems[parent, default: []].append(item)
                continue
            }
            topLevel.append(item)
        }
        var used: Set<String> = []
        func makeNode(_ item: ThreadItem) -> ChatNodeModel {
            let box = boxes[item.id] ?? ChatItemModel(item: item)
            box.update(item)
            boxes[item.id] = box
            used.insert(item.id)
            return ChatNodeModel(box: box, children: (childItems[item.id] ?? []).map(makeNode))
        }
        var rows: [ChatRowModel] = topLevel.map { .item(makeNode($0)) }
        for turn in transcript.turns where turn.status == .failed || turn.status == .interrupted {
            let last = rows.lastIndex { row in
                guard case .item(let node) = row else { return false }
                return node.box.item.turnId == turn.id
            }
            rows.insert(.turnEnded(turn), at: last.map { $0 + 1 } ?? rows.endIndex)
        }
        boxes = boxes.filter { used.contains($0.key) }
        return rows + transcript.pendingPrompts.map { .pending($0) }
    }
}
