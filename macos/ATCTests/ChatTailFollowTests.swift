import ATCDesign
import SwiftUI
import Testing

@testable import ATC

/// The Chat transcript's tail rule: the toolbar and composer bar are content
/// insets around the container, so "at the tail" means the container's bottom
/// reaches the content's end plus the composer inset — with a little slack.
@Suite("Chat tail follow")
struct ChatTailFollowTests {
    /// The geometry the app logged live at the tail after opening a thread:
    /// a 52pt toolbar and 114pt composer bar around a 728pt container, over
    /// 859pt of content — the offset is `-top` at rest at the top.
    private func geometry(offsetY: CGFloat) -> ScrollGeometry {
        ScrollGeometry(
            contentOffset: CGPoint(x: 0, y: offsetY),
            contentSize: CGSize(width: 1162, height: 859),
            contentInsets: EdgeInsets(top: 52, leading: 0, bottom: 114, trailing: 0),
            containerSize: CGSize(width: 1162, height: 728)
        )
    }

    @Test("the tail is where the container's bottom reaches the content end plus the composer inset")
    func tailDetection() {
        // 62 is where `scrollTo(tail, anchor: .bottom)` landed live: the
        // tail's 1pt and the 16pt bottom padding under the bar.
        #expect(geometry(offsetY: 62).isAtTail)
        // Fully scrolled: 62 + 17.
        #expect(geometry(offsetY: 79).isAtTail)
        // Within the slack.
        #expect(geometry(offsetY: 79 - Spacing.xxl).isAtTail)
        // Past the slack: the last rows are hidden under the composer.
        #expect(!geometry(offsetY: 79 - Spacing.xxl - 1).isAtTail)
        // At the top.
        #expect(!geometry(offsetY: -52).isAtTail)
    }

    @Test("a bar growing under the tail is a layout change that re-pins")
    func insetOnlyChangeIsALayoutChange() {
        let before = ChatTailLayout(geometry(offsetY: 62))
        // The composer grew by a line: only the bottom inset moved.
        let after = ChatTailLayout(
            ScrollGeometry(
                contentOffset: CGPoint(x: 0, y: 62),
                contentSize: CGSize(width: 1162, height: 859),
                contentInsets: EdgeInsets(top: 52, leading: 0, bottom: 138, trailing: 0),
                containerSize: CGSize(width: 1162, height: 728)
            ))
        #expect(before != after)
        // Scrolling alone is not.
        #expect(before == ChatTailLayout(geometry(offsetY: 0)))
    }

    @Test("only the user's own scrolling counts as a gesture")
    func gesturePhases() {
        #expect(ScrollPhase.tracking.isGesture)
        #expect(ScrollPhase.interacting.isGesture)
        #expect(ScrollPhase.decelerating.isGesture)
        #expect(!ScrollPhase.idle.isGesture)
        #expect(!ScrollPhase.animating.isGesture)
    }
}
