// The transcript as rows: what the Chat list actually renders. Items nest
// under their `parentItemId` (subagent / nested-tool work shows under the
// row that spawned it), a turn that failed or was interrupted gets one
// marker after its last item — the only place a turn boundary is visible —
// and pending prompts (the optimistic echo) sit at the tail until the server
// shows them somewhere real. Pure so the shape can be tested without views.

import ATCAppServerAPI
import Foundation

/// One transcript item with the items nested under it, to any depth.
struct ChatNode: Identifiable, Equatable {
    let item: ThreadItem
    let children: [ChatNode]

    var id: String { item.id }
}

enum ChatRow: Identifiable, Equatable {
    case item(ChatNode)
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

extension ChatTranscript {
    var rows: [ChatRow] {
        let ids = Set(items.map(\.id))
        var children: [String: [ThreadItem]] = [:]
        var topLevel: [ThreadItem] = []
        for item in items {
            // A parent that isn't on this page (older) leaves the child at
            // top level rather than dropping it.
            if let parent = item.parentItemId, ids.contains(parent) {
                children[parent, default: []].append(item)
                continue
            }
            topLevel.append(item)
        }
        func node(_ item: ThreadItem) -> ChatNode {
            ChatNode(item: item, children: (children[item.id] ?? []).map(node))
        }
        var rows: [ChatRow] = topLevel.map { .item(node($0)) }
        for turn in turns where turn.status == .failed || turn.status == .interrupted {
            let last = rows.lastIndex { row in
                guard case .item(let node) = row else { return false }
                return node.item.turnId == turn.id
            }
            rows.insert(.turnEnded(turn), at: last.map { $0 + 1 } ?? rows.endIndex)
        }
        return rows + pendingPrompts.map { .pending($0) }
    }
}
