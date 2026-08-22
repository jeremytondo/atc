import { Effect, Semaphore } from "effect"

/**
 * Per-key serialization: one permit per key, created on first use. `release`
 * drops a key's lock (a deleted row's) — a waiter still queued on it just
 * finds the row gone.
 */
export const makeKeyedLock = () => {
  const locks = new Map<string, Semaphore.Semaphore>()
  return {
    withLock:
      (key: string) =>
      <A, E, R>(effect: Effect.Effect<A, E, R>): Effect.Effect<A, E, R> =>
        Effect.suspend(() => {
          const existing = locks.get(key)
          if (existing !== undefined) return existing.withPermit(effect)
          const lock = Semaphore.makeUnsafe(1)
          locks.set(key, lock)
          return lock.withPermit(effect)
        }),
    release: (key: string): void => {
      locks.delete(key)
    },
  }
}
