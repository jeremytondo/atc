import Foundation

struct QueryMatch: Equatable {
    let ranges: [Range<String.Index>]
}

enum QueryMatcher {
    static func match(_ query: String, in title: String) -> QueryMatch? {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return QueryMatch(ranges: []) }

        if let range = title.range(of: trimmed, options: .caseInsensitive) {
            return QueryMatch(ranges: [range])
        }

        let wordStarts = wordStartIndices(in: title)
        let initials = String(wordStarts.compactMap {
            String(title[$0]).lowercased().first
        })
        guard let match = initials.range(of: trimmed.lowercased()) else { return nil }

        let offset = initials.distance(from: initials.startIndex, to: match.lowerBound)
        let length = initials.distance(from: match.lowerBound, to: match.upperBound)
        let ranges = wordStarts[offset..<(offset + length)].map { start in
            start..<title.index(after: start)
        }
        return QueryMatch(ranges: ranges)
    }

    private static func wordStartIndices(in title: String) -> [String.Index] {
        var starts: [String.Index] = []
        var isInsideWord = false

        for index in title.indices {
            let isAlphanumeric = title[index].unicodeScalars.allSatisfy {
                CharacterSet.alphanumerics.contains($0)
            }
            if isAlphanumeric, !isInsideWord {
                starts.append(index)
            }
            isInsideWord = isAlphanumeric
        }
        return starts
    }
}
