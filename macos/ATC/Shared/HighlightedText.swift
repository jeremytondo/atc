import SwiftUI

/// Search-match rendering shared by every search picker so highlighting
/// reads the same everywhere.
enum HighlightedText {
    /// A result's title with its matched ranges bolded, built as one flowing
    /// `Text` so a long title wraps as a unit (and can be composed with a
    /// category prefix).
    static func title(_ title: String, ranges: [Range<String.Index>]) -> Text {
        var text = Text("")
        var cursor = title.startIndex
        for range in ranges.sorted(by: { $0.lowerBound < $1.lowerBound }) {
            let plain = Text(String(title[cursor..<range.lowerBound]))
            let match = Text(String(title[range])).bold()
            text = Text("\(text)\(plain)\(match)")
            cursor = range.upperBound
        }
        return Text("\(text)\(Text(String(title[cursor...])))")
    }
}
