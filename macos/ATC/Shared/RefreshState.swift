// The one refresh discipline shared by the per-Connection domain stores:
// generation-guarded so a slow in-flight refresh never clobbers the result
// of a newer one, and failure-preserving — an error keeps last-loaded data
// and records the message instead of clearing state.

import Foundation
import OSLog

@Observable
final class RefreshState {
    private(set) var hasLoadedOnce = false
    private(set) var lastError: String?

    /// Whether reads reflect a current server model: loaded at least once
    /// and the newest refresh succeeded. Reconciliation and mutation gating
    /// must check this, never `hasLoadedOnce` alone — connection loss would
    /// otherwise look like deletion.
    var isResolved: Bool { hasLoadedOnce && lastError == nil }

    /// Monotonic token: only the newest in-flight refresh settles state.
    private var generation = 0

    /// A mutation merged a server response into the store: any refresh
    /// still in flight snapshotted the world before that response and must
    /// not overwrite it.
    func invalidateInFlight() {
        generation += 1
    }

    private let logger: Logger

    init(category: String) {
        logger = Logger(subsystem: "ElevenIdeas.atc", category: category)
    }

    /// Runs one refresh; `apply` runs only while this refresh is still the
    /// newest, so stale responses can never overwrite newer data.
    func run<Value>(
        _ fetch: () async throws -> Value,
        apply: (Value) -> Void
    ) async {
        generation += 1
        let current = generation
        do {
            let value = try await fetch()
            guard current == generation else { return }
            apply(value)
            lastError = nil
            hasLoadedOnce = true
        } catch {
            guard current == generation else { return }
            lastError = error.localizedDescription
            hasLoadedOnce = true
            logger.error("refresh failed: \(error)")
        }
    }
}
