import Foundation

/// A contract-modeled server failure rendered for alerts: the payload's
/// `message` is written for humans, so surfacing it beats the generated
/// throwing accessors' "Unexpected response…" dump. Stores switch on the
/// generated `Output` enum, pull `message` out of each documented failure
/// case, and throw this; `undocumented(status:)` covers the enum's
/// catch-all case.
struct ServerError: LocalizedError {
    let message: String

    var errorDescription: String? { message }

    static func undocumented(status: Int) -> ServerError {
        ServerError(message: "Unexpected server response (\(status)).")
    }
}
