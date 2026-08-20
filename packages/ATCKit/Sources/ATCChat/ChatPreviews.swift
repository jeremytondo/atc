// Chat previews over the recorded fixtures (ChatFixtures): the row stack
// exactly as ChatPane lays it out, without a server. This is where later
// rendering work is reviewed.

import ATCAppServerAPI
import ATCDesign
import SwiftUI

#if DEBUG
    private struct ChatFixturePreview: View {
        let rows: [ChatRowModel]

        init(page: ThreadTranscriptPage) {
            var transcript = ChatTranscript()
            transcript.load(page)
            var boxes: [String: ChatItemModel] = [:]
            rows = ChatRowBuilder.rows(from: transcript, reusing: &boxes)
        }

        var body: some View {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Spacing.md) {
                    ForEach(rows) { row in
                        ChatRowView(row: row)
                    }
                }
                .padding(Spacing.xxl)
                .frame(maxWidth: 820)
                .frame(maxWidth: .infinity)
            }
            .background(AppColors.canvas)
        }
    }

    #Preview("Claude transcript") {
        ChatFixturePreview(page: ChatFixtures.claude)
            .frame(width: 760, height: 900)
    }

    #Preview("Codex transcript") {
        ChatFixturePreview(page: ChatFixtures.codex)
            .frame(width: 760, height: 500)
    }
#endif
