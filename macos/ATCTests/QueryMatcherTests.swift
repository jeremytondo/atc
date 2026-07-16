import Testing
@testable import ATC

@Suite("Command palette query matcher")
struct QueryMatcherTests {
    @Test("empty and whitespace-only queries match without highlights")
    func emptyQueries() throws {
        #expect(try #require(QueryMatcher.match("", in: "Toggle Sidebar")).ranges.isEmpty)
        #expect(try #require(QueryMatcher.match("  \n", in: "New Project…")).ranges.isEmpty)
    }

    @Test("substring matching is case-insensitive and returns the exact range")
    func substring() throws {
        let title = "Toggle Sidebar"
        let match = try #require(QueryMatcher.match("SID", in: title))
        #expect(strings(for: match, in: title) == ["Sid"])
    }

    @Test("word-initial matching covers representative command titles")
    func wordInitials() throws {
        for (query, title, expected) in [
            ("np", "New Project…", ["N", "P"]),
            ("nt", "New Terminal", ["N", "T"]),
            ("ts", "Toggle Sidebar", ["T", "S"]),
            ("rc", "Reload Configuration", ["R", "C"]),
        ] {
            let match = try #require(QueryMatcher.match(query, in: title))
            #expect(strings(for: match, in: title) == expected)
        }
    }

    @Test("punctuation does not form words")
    func punctuation() throws {
        let title = "New & Project…"
        let match = try #require(QueryMatcher.match("np", in: title))
        #expect(strings(for: match, in: title) == ["N", "P"])
        #expect(QueryMatcher.match("n&p", in: title) == nil)
    }

    @Test("substring matching wins when both strategies match")
    func substringPrecedence() throws {
        let title = "To Optimize"
        let match = try #require(QueryMatcher.match("to", in: title))
        #expect(strings(for: match, in: title) == ["To"])
    }

    @Test("nonmatches return nil")
    func nonmatch() {
        #expect(QueryMatcher.match("xyz", in: "Toggle Sidebar") == nil)
    }

    private func strings(for match: QueryMatch, in title: String) -> [String] {
        match.ranges.map { String(title[$0]) }
    }
}
