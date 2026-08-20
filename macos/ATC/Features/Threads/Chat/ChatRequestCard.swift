// A pending provider request as a card above the composer — the bar's home
// for requests that name no transcript item (`ChatRequestContentView` in
// ATCChat carries the actual question/approval content; requests with an
// item render inline on that row instead). Floats over the transcript with
// the composer, so glass like it.

import ATCAppServerAPI
import ATCChat
import ATCDesign
import SwiftUI

struct ChatRequestCard: View {
    let request: ThreadRequest
    let answer: (ThreadRequestAnswer) -> Void

    var body: some View {
        ChatRequestContentView(request: request, answer: answer)
            .padding(Spacing.lg)
            .glassEffect(in: RoundedRectangle(cornerRadius: Radius.card + Spacing.xs))
    }
}
