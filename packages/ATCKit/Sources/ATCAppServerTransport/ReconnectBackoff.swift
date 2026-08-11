// The shared reconnect schedule: the SSE resource stream uses it here, and
// the app-level attach RetryPolicy delegates to it. It is a pure function of
// the attempt number, so callers keep their own counter, their own starting
// index (attempt 0 = base for the attach path; the SSE stream counts its
// first retry as attempt 1 — both pre-existing schedules, preserved), and
// their own rule for resetting (only once a connection has proven stable; a
// server that greets and immediately drops must not defeat the escalation).
//
// Arithmetic is in whole milliseconds: the doubling is capped at every step,
// jitter is applied to the capped value, and the result is capped again so a
// positive jitter never exceeds the advertised maximum. Sub-millisecond
// configuration rounds to zero — configure in milliseconds or coarser.

import Foundation

public struct ReconnectBackoff: Sendable, Equatable {
    public let baseDelay: Duration
    public let maximumDelay: Duration
    /// Symmetric jitter as a fraction of the nominal delay. Zero is exact
    /// doubling, which is what a transport wants when its own timing is
    /// already spread out by the server.
    public let jitterFraction: Double

    public init(baseDelay: Duration, maximumDelay: Duration, jitterFraction: Double = 0) {
        self.baseDelay = baseDelay
        self.maximumDelay = maximumDelay
        self.jitterFraction = jitterFraction
    }

    /// The delay before retry `attempt` (zero-based): the base delay doubled
    /// `attempt` times. `jitterUnit` (clamped to 0...1) places the sample
    /// inside the jitter window — 0.5 is the nominal delay, 0 and 1 its ends.
    public func delay(forAttempt attempt: Int, jitterUnit: Double = 0.5) -> Duration {
        let maximum = Self.milliseconds(of: maximumDelay)
        var nominal = Self.milliseconds(of: baseDelay)
        for _ in 0..<max(0, attempt) {
            nominal = min(maximum, nominal * 2)
            if nominal >= maximum { break }
        }

        let unit = min(1, max(0, jitterUnit))
        let factor = (1 - jitterFraction) + (2 * jitterFraction * unit)
        let jittered = Int64((Double(nominal) * factor).rounded())
        return .milliseconds(min(maximum, max(0, jittered)))
    }

    private static func milliseconds(of duration: Duration) -> Int64 {
        let components = duration.components
        return components.seconds * 1_000
            + Int64((Double(components.attoseconds) / 1_000_000_000_000_000).rounded())
    }
}
