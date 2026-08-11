import { Effect } from "effect"
import type { Schedule } from "effect"

// The one demand-polling loop: run `check` immediately, and while `until`
// rejects its value, wait out the schedule and check again. Resolves with
// the last checked value once `until` accepts it or the schedule ends —
// polling never fails by itself, so what a rejected final value means
// stays with the caller. Cadence is the caller's Schedule (spaced, capped
// exponential, bounded with upTo — see the call sites).

export const pollUntil = <A, E = never, R = never>(
  check: Effect.Effect<A, E, R>,
  options: {
    readonly until: (value: A) => boolean
    readonly schedule: Schedule.Schedule<unknown, A>
  },
): Effect.Effect<A, E, R> =>
  Effect.repeat(check, { schedule: options.schedule, until: options.until })
