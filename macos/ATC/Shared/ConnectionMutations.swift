import SwiftUI

/// The one shared wrapper for Connection-backed mutations: refuses when the
/// Connection's model isn't current and routes failures into the caller's
/// alert state. Views must not hand-roll this guard.
extension AppModel {
    func run(
        on connectionID: UUID,
        reporting actionError: Binding<String?>,
        _ operation: @escaping () async throws -> Void
    ) {
        Task {
            guard canMutate(connectionID: connectionID) else {
                actionError.wrappedValue = "The connection is unavailable."
                return
            }
            do {
                try await operation()
            } catch {
                actionError.wrappedValue = error.localizedDescription
            }
        }
    }
}
