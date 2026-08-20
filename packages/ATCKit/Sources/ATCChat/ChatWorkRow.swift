// One turn's work as a single fold, the way Codex desktop shows it. While
// the turn runs the fold stays open enough to be alive: the newest step is
// visible under the header and older steps compress to "+N earlier steps"
// (opening the fold shows them all). Once the turn settles the fold
// collapses to its one-line summary — "Worked for 1m 12s · 14 steps" — and
// reopens on demand. Expansion lives on the `ChatWorkModel` box so rebuilds
// and list recycling cannot reset it.

import ATCAppServerAPI
import ATCDesign
import SwiftUI

struct ChatWorkRow: View {
    @Bindable var work: ChatWorkModel

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            header
            if work.isExpanded {
                steps(work.steps)
            } else if work.isRunning, let live = work.steps.last {
                steps([live])
            }
        }
    }

    private var header: some View {
        Button {
            withAnimation(.snappy(duration: 0.2)) { work.isExpanded.toggle() }
        } label: {
            HStack(spacing: Spacing.sm) {
                if work.isRunning {
                    ProgressView().controlSize(.mini)
                        .frame(width: 14)
                } else {
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.semibold))
                        .rotationEffect(.degrees(work.isExpanded ? 90 : 0))
                        .frame(width: 14)
                }
                Text(summary)
                    .font(.callout)
                if work.isRunning, !work.isExpanded, work.steps.count > 1 {
                    Text("+\(work.steps.count - 1) earlier \(work.steps.count == 2 ? "step" : "steps")")
                        .font(.caption)
                        .padding(.horizontal, Spacing.xs)
                        .padding(.vertical, 2)
                        .background(Surface.raised, in: Capsule())
                }
                Spacer(minLength: 0)
            }
            .foregroundStyle(.secondary)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(work.isExpanded ? "Collapse the turn's work" : "Show the turn's work")
    }

    /// "Working…" while running; "Worked for 1m 12s · 14 steps" once the
    /// turn settled (duration only when the page carries the turn).
    private var summary: String {
        let count = work.steps.count
        let steps = "\(count) \(count == 1 ? "step" : "steps")"
        guard !work.isRunning else { return "Working…" }
        guard let turn = work.turn, let started = turn.startedAt, let ended = turn.endedAt else {
            return steps
        }
        return "Worked for \(Self.duration(from: started, to: ended)) · \(steps)"
    }

    private func steps(_ nodes: [ChatNodeModel]) -> some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            ForEach(nodes) { node in
                ChatNodeView(node: node)
            }
        }
        .padding(.leading, Spacing.md + Spacing.sm)
    }

    /// A worked-for duration: "4s", "1m 12s", "2h 3m".
    static func duration(from start: Date, to end: Date) -> String {
        let seconds = max(0, Int(end.timeIntervalSince(start).rounded()))
        let (hours, minutes) = (seconds / 3600, (seconds % 3600) / 60)
        if hours > 0 { return "\(hours)h \(minutes)m" }
        if minutes > 0 { return "\(minutes)m \(seconds % 60)s" }
        return "\(seconds)s"
    }
}
