import Foundation

/// Suspends until `condition` holds, giving the transport's own tasks room to
/// run. `attempts` is a failure bound, never the thing under test: every
/// caller waits on a real observable condition, and a passing test returns as
/// soon as it holds.
func waitUntil(
    attempts: Int = 2_000,
    _ condition: () -> Bool
) async -> Bool {
    for _ in 0..<attempts {
        if condition() { return true }
        try? await Task.sleep(for: .milliseconds(1))
    }
    return condition()
}
