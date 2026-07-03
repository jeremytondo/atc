import Foundation
import Testing
@testable import CockpitAPI

@Suite("RFC3339Nano date decoding")
struct DateDecodingTests {
    struct Box: Decodable {
        var date: Date
    }

    func decode(_ dateString: String) throws -> Date {
        let json = Data(#"{"date":"\#(dateString)"}"#.utf8)
        return try JSONDecoder.cockpit().decode(Box.self, from: json).date
    }

    @Test("zero fractional digits")
    func zeroDigits() throws {
        let date = try decode("2026-06-30T03:30:53Z")
        #expect(abs(date.timeIntervalSince1970 - 1782790253) < 0.001)
    }

    @Test("three fractional digits")
    func threeDigits() throws {
        let date = try decode("2026-06-30T03:30:53.536Z")
        #expect(abs(date.timeIntervalSince1970 - 1782790253.536) < 0.001)
    }

    @Test("six fractional digits")
    func sixDigits() throws {
        let date = try decode("2026-06-30T03:30:53.536616Z")
        #expect(abs(date.timeIntervalSince1970 - 1782790253.5366) < 0.001)
    }

    @Test("nine fractional digits — as the live server sends")
    func nineDigits() throws {
        let date = try decode("2026-06-30T03:30:53.536616153Z")
        #expect(abs(date.timeIntervalSince1970 - 1782790253.5366) < 0.001)
    }

    @Test("offset timezone form")
    func offsetTimezone() throws {
        let utc = try decode("2026-06-30T03:30:53Z")
        let offset = try decode("2026-06-30T05:30:53+02:00")
        #expect(utc == offset)
    }

    @Test("garbage throws")
    func garbage() {
        #expect(throws: (any Error).self) {
            _ = try decode("not-a-date")
        }
    }
}
