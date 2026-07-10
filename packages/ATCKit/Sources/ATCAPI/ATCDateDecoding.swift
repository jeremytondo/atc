import Foundation

public extension JSONDecoder.DateDecodingStrategy {
    /// Go's RFC3339Nano emits 0–9 fractional digits; `ISO8601DateFormatter`
    /// only handles exactly 0 or 3, but `parseStrategy`-based ISO-8601 parsing
    /// accepts variable-length fractions.
    static let atcRFC3339Nano = JSONDecoder.DateDecodingStrategy.custom { decoder in
        let container = try decoder.singleValueContainer()
        let string = try container.decode(String.self)
        if let date = try? Date(string, strategy: .iso8601.time(includingFractionalSeconds: true)) {
            return date
        }
        if let date = try? Date(string, strategy: .iso8601) {
            return date
        }
        throw DecodingError.dataCorruptedError(
            in: container,
            debugDescription: "Unparseable RFC3339Nano date: \(string)"
        )
    }
}

extension JSONDecoder {
    /// Decoder configured for atc server responses.
    static func atc() -> JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .atcRFC3339Nano
        return decoder
    }
}
