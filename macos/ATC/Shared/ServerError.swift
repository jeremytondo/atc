import ATCAppServerAPI
import Foundation

/// A contract-modeled server failure rendered for alerts: the payload's
/// `message` is written for humans, so surfacing it beats the generated
/// throwing accessors' "Unexpected response…" dump. Stores switch on the
/// generated `Output` enum, pull `message` out of each documented failure
/// case through the `ServerError(...)` initializers below, and throw this;
/// `undocumented(status:)` covers the enum's catch-all case.
struct ServerError: LocalizedError {
    let message: String

    var errorDescription: String? { message }

    static func undocumented(status: Int) -> ServerError {
        ServerError(message: "Unexpected server response (\(status)).")
    }
}

/// Every documented failure schema carries the server's human-readable
/// `message` — the one field this seam needs. Conformances are added on
/// demand: a schema without one fails to compile at its first unwrap site.
/// (A NEW branch appended to an existing anyOf status is the blind spot —
/// the variadic initializer below cannot see a `valueN` a call site forgot
/// to pass, so contract changes to anyOf statuses need their unwrap sites
/// re-checked by hand.)
nonisolated protocol ServerErrorPayload {
    var message: String { get }
}

extension Components.Schemas.ProjectNotFoundJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.DirectoryUnavailableJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.DirectoryCheckTimedOutJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.TerminalNotFoundJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.ZmxUnavailableJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.TerminalLaunchFailedJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.ThreadNotFoundJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.ThreadArchivedJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.ThreadBusyJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.ProviderUnavailableJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.ProviderSessionConflictJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.RequestNotFoundJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.InvalidRequestAnswerJsonEncoding: ServerErrorPayload {}
extension Components.Schemas.QueuedPromptNotFoundJsonEncoding: ServerErrorPayload {}

extension ServerError {
    /// Wrap a single-schema failure payload.
    init(_ payload: some ServerErrorPayload) {
        self.init(message: payload.message)
    }

    /// Wrap an anyOf failure payload (a status documented with several
    /// tagged errors): the first populated branch's message. The generated
    /// decoder guarantees at least one branch is populated.
    init(anyOf first: (any ServerErrorPayload)?, _ rest: (any ServerErrorPayload)?...) {
        let branches = [first] + rest
        self.init(message: branches.compactMap { $0?.message }.first ?? "Unknown server error.")
    }
}
