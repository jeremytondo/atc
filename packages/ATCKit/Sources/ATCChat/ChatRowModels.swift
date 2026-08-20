// The transcript as render rows (ATC-214/215): what the Chat list shows,
// built by `ChatRowBuilder` from the reducer's copy. The visual model is
// Codex desktop's: the prompt, one work fold per turn holding the tool and
// reasoning steps, and the answer as prose — so a 30-step turn reads as
// three rows, not thirty. Items nest under their `parentItemId` (subagent /
// nested-tool work shows under the row that spawned it), a turn that failed
// or was interrupted gets one marker after its last row (with the prompt to
// retry), and pending prompts (the optimistic echo) sit at the tail.
//
// Rows hold reference boxes (`ChatItemModel` per item, `ChatWorkModel` per
// turn's fold), which is what the row views observe — a text delta
// invalidates exactly one row instead of the whole pane. Boxes also own the
// per-row view state that must survive LazyVStack recycling and structural
// rebuilds — disclosure and fold expansion — plus the render memos: pretty
// JSON and parsed markdown per item version, and the streaming markdown
// cache that re-parses only the trailing block. The builder reuses boxes by
// id, so identity (and expansion) survives every rebuild.
//
// Pending approvals and questions attach to the row of the item they block
// (`ThreadRequest.itemId`); the builder reports which requests it attached
// so the model can keep only the rest in the bottom bar.

import ATCAppServerAPI
import Foundation
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
    /// Markdown for the completed text, parsed once per item version.
    @ObservationIgnored private var completedBlocks: [MarkdownBlock]?
    /// Markdown for streaming text: each delta re-parses only the trailing
    /// block. Survives item updates by design.
    @ObservationIgnored private var streamCache = MarkdownStreamCache()

    init(item: ThreadItem) {
        self.item = item
    }

    var id: String { item.id }

    func update(_ item: ThreadItem) {
        guard self.item != item else { return }
        self.item = item
        prettyCache = [:]
        completedBlocks = nil
    }

    /// The pretty JSON of one of this item's fields ("input", "output",
    /// "arguments", "result"), computed once per item version.
    func pretty(_ field: String, _ value: OpenAPIValueContainer?) -> String {
        if let cached = prettyCache[field] { return cached }
        let rendered = PrettyJSON.string(value)
        prettyCache[field] = rendered
        return rendered
    }

    /// This item's text as markdown blocks. Streaming text goes through the
    /// tail-only re-parse; completed text is fully parsed once and memoized
    /// per item version.
    func markdownBlocks(text: String, complete: Bool) -> [MarkdownBlock] {
        guard complete else { return streamCache.update(text) }
        if let completedBlocks { return completedBlocks }
        let blocks = MarkdownParser.blocks(from: text)
        completedBlocks = blocks
        return blocks
    }
}

/// One turn's work fold: the reasoning/tool steps between the prompt and
/// the answer, summarized to one line ("Worked for 1m 12s · 14 steps").
@Observable
final class ChatWorkModel: Identifiable {
    let turnID: String
    /// The turn, when the loaded page carries it (it may be off-page for
    /// old history); without it the fold still lists its steps.
    private(set) var turn: ThreadTurn?
    private(set) var steps: [ChatNodeModel] = []
    /// Reopenable after the turn settles; while running the fold shows the
    /// live step regardless.
    var isExpanded = false

    init(turnID: String) {
        self.turnID = turnID
    }

    var id: String { turnID }

    var isRunning: Bool { turn?.status == .running }

    func update(turn: ThreadTurn?, steps: [ChatNodeModel]) {
        if self.turn != turn { self.turn = turn }
        self.steps = steps
    }
}

/// One rendered node: the observed box, its nested children, and the
/// pending request blocked on it, if any.
struct ChatNodeModel: Identifiable {
    let box: ChatItemModel
    let children: [ChatNodeModel]
    let request: ThreadRequest?
    /// The item's turn is not running (an off-page turn counts as settled):
    /// timestamps show only then, so nothing flashes mid-stream.
    let turnIsSettled: Bool

    var id: String { box.id }
}

enum ChatRowModel: Identifiable {
    /// A prompt, an answer, or a compaction divider — prose at top level.
    case item(ChatNodeModel)
    /// A turn's folded work.
    case work(ChatWorkModel)
    /// A turn that ended abnormally, placed after its last row.
    case turnEnded(ChatTurnEnded)
    /// A prompt sent from this client that the transcript does not carry yet.
    case pending(PendingPrompt)

    var id: String {
        switch self {
        case .item(let node): "item:\(node.id)"
        case .work(let work): "work:\(work.turnID)"
        case .turnEnded(let ended): "turn:\(ended.turn.id)"
        case .pending(let prompt): "pending:\(prompt.id)"
        }
    }
}

/// A failed or interrupted turn's marker, carrying the prompt Retry
/// re-sends when the turn's user message is on the page.
struct ChatTurnEnded: Identifiable {
    let turn: ThreadTurn
    let retryPrompt: String?

    var id: String { turn.id }
}

enum ChatRowBuilder {
    struct Result {
        var rows: [ChatRowModel] = []
        /// Requests rendered on their item's row; the rest stay in the bar.
        var attachedRequestIDs: Set<String> = []
    }

    /// The render rows for the copy as it stands, reusing item boxes and
    /// work folds by id so expansion state survives. Boxes for items or
    /// turns no longer present are dropped.
    static func rows(
        from transcript: ChatTranscript,
        reusing boxes: inout [String: ChatItemModel],
        folds: inout [String: ChatWorkModel]
    ) -> Result {
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
        var requestsByItem: [String: ThreadRequest] = [:]
        for request in transcript.requests {
            guard let itemId = request.itemId, ids.contains(itemId) else { continue }
            requestsByItem[itemId] = request
        }

        var result = Result()
        var usedBoxes: Set<String> = []
        var usedFolds: Set<String> = []
        func makeNode(_ item: ThreadItem) -> ChatNodeModel {
            let box = boxes[item.id] ?? ChatItemModel(item: item)
            box.update(item)
            boxes[item.id] = box
            usedBoxes.insert(item.id)
            let request = requestsByItem[item.id]
            if let request { result.attachedRequestIDs.insert(request.id) }
            return ChatNodeModel(
                box: box,
                children: (childItems[item.id] ?? []).map(makeNode),
                request: request,
                turnIsSettled: transcript.turn(id: item.turnId).map { $0.status != .running } ?? true
            )
        }

        // Steps fold per turn; prose stays a row. A turn's fold sits where
        // its first step happened.
        var stepsByTurn: [String: [ChatNodeModel]] = [:]
        for item in topLevel {
            let node = makeNode(item)
            guard item.isWorkStep else {
                result.rows.append(.item(node))
                continue
            }
            if stepsByTurn[item.turnId] == nil {
                let fold = folds[item.turnId] ?? ChatWorkModel(turnID: item.turnId)
                folds[item.turnId] = fold
                usedFolds.insert(item.turnId)
                result.rows.append(.work(fold))
            }
            stepsByTurn[item.turnId, default: []].append(node)
        }
        for (turnID, steps) in stepsByTurn {
            folds[turnID]?.update(turn: transcript.turn(id: turnID), steps: steps)
        }

        for turn in transcript.turns where turn.status == .failed || turn.status == .interrupted {
            let last = result.rows.lastIndex { $0.turnID == turn.id }
            let prompt = topLevel.last { item in
                guard case .userMessage(let message) = item else { return false }
                return message.turnId == turn.id
            }
            let marker = ChatTurnEnded(
                turn: turn,
                retryPrompt: prompt.flatMap { item in
                    guard case .userMessage(let message) = item else { return nil }
                    return message.text
                }
            )
            result.rows.insert(.turnEnded(marker), at: last.map { $0 + 1 } ?? result.rows.endIndex)
        }

        boxes = boxes.filter { usedBoxes.contains($0.key) }
        folds = folds.filter { usedFolds.contains($0.key) }
        result.rows += transcript.pendingPrompts.map { .pending($0) }
        return result
    }
}

extension [ChatRowModel] {
    /// The scrollable row that shows `itemID`, opening the work fold and
    /// every disclosure above the item on the way so the row the caller
    /// scrolls to actually shows it. Nil when the item is not on this page.
    func reveal(itemID: String) -> String? {
        for row in self {
            switch row {
            case .item(let node):
                if node.reveal(itemID: itemID) { return row.id }
            case .work(let work):
                if work.steps.contains(where: { $0.reveal(itemID: itemID) }) {
                    work.isExpanded = true
                    return row.id
                }
            case .turnEnded, .pending:
                continue
            }
        }
        return nil
    }
}

extension ChatNodeModel {
    /// True when this subtree holds `itemID`; opens the disclosures on the
    /// way down so the item is visible.
    fileprivate func reveal(itemID: String) -> Bool {
        if id == itemID { return true }
        guard children.contains(where: { $0.reveal(itemID: itemID) }) else { return false }
        box.isExpanded = true
        return true
    }
}

extension ChatRowModel {
    /// The turn a row belongs to, for marker placement; nil for pending
    /// echoes (not on the server yet).
    fileprivate var turnID: String? {
        switch self {
        case .item(let node): node.box.item.turnId
        case .work(let work): work.turnID
        case .turnEnded(let ended): ended.turn.id
        case .pending: nil
        }
    }
}

extension ThreadItem {
    /// Steps fold into the turn's work row; prose and dividers stay rows.
    var isWorkStep: Bool {
        switch self {
        case .reasoning, .command, .fileChange, .mcpCall, .toolCall: true
        case .userMessage, .assistantText, .compaction: false
        }
    }
}
