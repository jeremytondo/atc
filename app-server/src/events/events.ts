import { Context, Effect, Layer, Queue, Stream } from "effect"
import type { Cause } from "effect"
import type { ResourceChangedEvent as ResourceChangedEventSchema } from "../api/contract.ts"

export type ResourceChangedEvent = typeof ResourceChangedEventSchema.Type

// The Events service (ATC-128): the in-process fan-out for resource-change
// events. Domain services publish next to each successful mutation; the
// subscribe endpoint streams to clients. Invariants:
//
//   - Delivery is ephemeral, best-effort: no log, no replay, and publishes
//     sit after their durable write outside any uninterruptible region, so
//     a crash or abort in between can drop an event. Clients resync on
//     every (re)connect, which heals every drop the service can detect by
//     ending the stream — the design contract is "close the stream on any
//     doubt".
//   - publish never blocks or fails: each subscriber has its own bounded
//     queue, and a full queue ends that subscriber's stream (the client's
//     reconnect resync heals it) rather than back-pressuring mutations.
//   - subscribe() registers eagerly, in the effect: by the time it returns,
//     the subscriber receives every subsequent publish. The subscribe
//     endpoint relies on this to guarantee "registered before the client
//     sees any byte". A registered stream that never runs self-heals: its
//     queue fills and publish drops it.
//   - close() (also the layer finalizer) ends every subscriber stream
//     cleanly. server.ts sequences it to release BEFORE the HTTP listener's
//     bounded graceful stop — an open SSE response would otherwise hold the
//     stop for the full timeout and die on the noisy forced-interrupt path.

export class Events extends Context.Service<
  Events,
  {
    /** Deliver one event to every current subscriber; never blocks. */
    readonly publish: (event: ResourceChangedEvent) => Effect.Effect<void>
    /**
     * Register a subscriber (eagerly — see the header) and return its live
     * stream, which ends on lag or on close().
     */
    readonly subscribe: () => Effect.Effect<Stream.Stream<ResourceChangedEvent>>
    /** Currently registered subscribers. Exists for tests and diagnostics. */
    readonly subscriberCount: () => Effect.Effect<number>
    /** End every subscriber stream, current and future. Idempotent. */
    readonly close: () => Effect.Effect<void>
  }
>()("app-server/Events") {}

export interface EventsOptions {
  /** Per-subscriber queue capacity; overflow ends that subscriber's stream. */
  readonly subscriberCapacity?: number
}

export const layerWith = (options: EventsOptions) =>
  Layer.effect(Events)(
    Effect.gen(function* () {
      const capacity = options.subscriberCapacity ?? 128
      const subscribers = new Set<Queue.Queue<ResourceChangedEvent, Cause.Done>>()
      let closed = false

      const close = () =>
        Effect.sync(() => {
          closed = true
          for (const queue of subscribers) Queue.endUnsafe(queue)
          subscribers.clear()
        })

      // Belt and braces: the server drains explicitly before the listener
      // stops (server.ts); this covers every other lifecycle (tests, crashes
      // of sibling layers) so no subscriber stream outlives the service.
      yield* Effect.addFinalizer(close)

      return {
        publish: (event) =>
          Effect.sync(() => {
            for (const queue of subscribers) {
              if (Queue.offerUnsafe(queue, event)) continue
              // Ended queues buffered events the subscriber still drains;
              // this one lost `event`, so resync-on-reconnect is the only
              // honest recovery.
              Queue.endUnsafe(queue)
              subscribers.delete(queue)
            }
          }),
        subscribe: () =>
          Effect.gen(function* () {
            const queue = yield* Queue.make<ResourceChangedEvent, Cause.Done>({ capacity })
            if (closed) {
              yield* Queue.end(queue)
            } else {
              subscribers.add(queue)
            }
            return Stream.fromQueue(queue).pipe(
              Stream.ensuring(Effect.sync(() => subscribers.delete(queue))),
            )
          }),
        subscriberCount: () => Effect.sync(() => subscribers.size),
        close,
      }
    }),
  )

export const layer = layerWith({})
