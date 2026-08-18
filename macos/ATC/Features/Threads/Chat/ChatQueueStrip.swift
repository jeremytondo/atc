// Prompts admitted while a turn was running, waiting for the next idle: a
// compact strip above the composer, one row per queued prompt with ✕ to
// withdraw it. Queued prompts are live state, never transcript items.

import ATCAppServerAPI
import SwiftUI

struct ChatQueueStrip: View {
    let prompts: [QueuedPrompt]
    let withdraw: (QueuedPrompt) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(prompts, id: \.id) { prompt in
                HStack(spacing: Spacing.sm) {
                    Image(systemName: "clock")
                        .foregroundStyle(.secondary)
                    Text(prompt.prompt)
                        .lineLimit(1)
                        .truncationMode(.tail)
                    Spacer(minLength: 0)
                    Button {
                        withdraw(prompt)
                    } label: {
                        Image(systemName: "xmark")
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .help("Withdraw this prompt")
                }
                .font(.callout)
                .padding(.horizontal, Spacing.md)
                .padding(.vertical, Spacing.sm)
            }
        }
        // One glass strip floating with the composer, not a chip per row.
        .glassEffect(in: RoundedRectangle(cornerRadius: Radius.card + Spacing.xs))
    }
}
