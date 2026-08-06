import { assert, describe, it } from "@effect/vitest"
import { Deferred, Effect, Fiber, Stream } from "effect"
import * as Events from "../../src/events/events.ts"
import type { ResourceChangedEvent } from "../../src/events/events.ts"

// Events service semantics: fan-out delivery, the overflow-closes-the-lagging-
// subscriber rule, and the clean-completion guarantees subscribers rely on
// for reconnect-and-resync.

const capacity = 2
const layer = Events.layerWith({ subscriberCapacity: capacity })

const event = (id: string): ResourceChangedEvent => ({
  resource: "project",
  id,
  change: "updated",
})

/** Poll (assertion-bounded) until `condition` holds; no timers involved. */
const waitFor = (condition: Effect.Effect<boolean>, label: string) =>
  Effect.gen(function* () {
    for (let attempt = 0; !(yield* condition); attempt++) {
      assert.isBelow(attempt, 1000, `never observed: ${label}`)
      yield* Effect.yieldNow
    }
  })

// Heartbeat ticks share the subscriber queues (see the module header); the
// event-delivery tests filter them out so slow CI runs cannot interleave one.
const collectInto = (events: Events.Events["Service"], sink: Array<ResourceChangedEvent>) =>
  events
    .subscribe()
    .pipe(
      Effect.flatMap(
        Stream.runForEach((received) =>
          Effect.sync(() => {
            if (received !== Events.HEARTBEAT) sink.push(received)
          }),
        ),
      ),
    )

describe("Events", () => {
  it.effect("delivers published events to every subscriber, in order", () =>
    Effect.gen(function* () {
      const events = yield* Events.Events
      const first: Array<ResourceChangedEvent> = []
      const second: Array<ResourceChangedEvent> = []
      const firstFiber = yield* Effect.forkChild(collectInto(events, first))
      const secondFiber = yield* Effect.forkChild(collectInto(events, second))
      yield* waitFor(
        Effect.map(events.subscriberCount(), (count) => count === 2),
        "both subscribers registered",
      )

      yield* events.publish(event("a"))
      yield* events.publish(event("b"))
      yield* waitFor(
        Effect.sync(() => first.length === 2 && second.length === 2),
        "both received both events",
      )
      assert.deepStrictEqual(first, [event("a"), event("b")])
      assert.deepStrictEqual(second, [event("a"), event("b")])

      // close() completes every stream cleanly — the collectors return.
      yield* events.close()
      yield* Fiber.join(firstFiber)
      yield* Fiber.join(secondFiber)
    }).pipe(Effect.provide(layer)),
  )

  it.effect("overflow ends only the lagging subscriber's stream", () =>
    Effect.gen(function* () {
      const events = yield* Events.Events
      const gate = yield* Deferred.make<void>()
      const lagging: Array<ResourceChangedEvent> = []
      const healthy: Array<ResourceChangedEvent> = []
      // The lagging consumer records each event, then parks until the gate
      // opens — events pile up in its bounded queue behind the parked pull.
      const laggingFiber = yield* Effect.forkChild(
        events
          .subscribe()
          .pipe(
            Effect.flatMap(
              Stream.runForEach((received) =>
                received === Events.HEARTBEAT
                  ? Effect.void
                  : Effect.sync(() => lagging.push(received)).pipe(
                      Effect.andThen(Deferred.await(gate)),
                    ),
              ),
            ),
          ),
      )
      const healthyFiber = yield* Effect.forkChild(collectInto(events, healthy))
      yield* waitFor(
        Effect.map(events.subscriberCount(), (count) => count === 2),
        "both subscribers registered",
      )

      // Park the lagging consumer on its first event, then keep publishing
      // until its bounded queue overflows and the service drops it. How many
      // events fit before that (queue capacity plus the stream's pull-ahead)
      // is an implementation detail; the contract is that a bounded number
      // suffices and only the lagging subscriber is dropped.
      yield* events.publish(event("fill-0"))
      yield* waitFor(
        Effect.sync(() => lagging.length === 1),
        "the lagging consumer pulled the first event",
      )
      let published = 1
      while ((yield* events.subscriberCount()) === 2) {
        assert.isBelow(published, capacity + 5, "overflow never dropped the lagging subscriber")
        yield* events.publish(event(`fill-${published}`))
        published++
        // Let the healthy consumer drain between publishes — only the
        // parked subscriber may fall behind.
        yield* Effect.yieldNow
      }
      assert.strictEqual(yield* events.subscriberCount(), 1)

      // The lagging stream ends cleanly after draining what its queue still
      // held — a strict prefix of the feed, missing at least the overflowed
      // tail — so its consumer can reconnect and resync.
      yield* Deferred.succeed(gate, undefined)
      yield* Fiber.join(laggingFiber)
      assert.isBelow(lagging.length, published)
      assert.deepStrictEqual(
        lagging.map((received) => received.id),
        Array.from({ length: lagging.length }, (_, sequence) => `fill-${sequence}`),
      )

      // The healthy subscriber saw everything and keeps receiving.
      yield* events.publish(event("after"))
      yield* waitFor(
        Effect.sync(() => healthy.length === published + 1),
        "the healthy subscriber kept the feed",
      )
      assert.deepStrictEqual(
        healthy.map((received) => received.id),
        [...Array.from({ length: published }, (_, sequence) => `fill-${sequence}`), "after"],
      )
      yield* events.close()
      yield* Fiber.join(healthyFiber)
    }).pipe(Effect.provide(layer)),
  )

  it.effect("subscribing after close yields an immediately ended stream", () =>
    Effect.gen(function* () {
      const events = yield* Events.Events
      yield* events.close()
      const received = yield* Effect.flatMap(events.subscribe(), Stream.runCollect)
      assert.deepStrictEqual(received, [])
    }).pipe(Effect.provide(layer)),
  )

  // it.live: the heartbeat timer runs on the real clock.
  it.live("one shared timer ticks a heartbeat to every subscriber", () =>
    Effect.gen(function* () {
      const events = yield* Events.Events
      const counts = [0, 0]
      const fibers = yield* Effect.forEach(counts.keys(), (index) =>
        Effect.forkChild(
          events
            .subscribe()
            .pipe(
              Effect.flatMap(
                Stream.runForEach((received) =>
                  Effect.sync(() => {
                    if (received === Events.HEARTBEAT) counts[index]!++
                  }),
                ),
              ),
            ),
        ),
      )
      yield* waitFor(
        Effect.map(events.subscriberCount(), (count) => count === 2),
        "both subscribers registered",
      )
      for (let attempt = 0; !counts.every((count) => count >= 2); attempt++) {
        assert.isBelow(attempt, 1000, `heartbeats never arrived; saw ${JSON.stringify(counts)}`)
        yield* Effect.sleep("10 millis")
      }
      yield* events.close()
      yield* Effect.forEach(fibers, Fiber.join)
    }).pipe(Effect.provide(Events.layerWith({ heartbeatInterval: "20 millis" }))),
  )
})
