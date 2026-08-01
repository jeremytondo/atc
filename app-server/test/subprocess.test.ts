import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Deferred, Effect, Exit, Fiber, Layer, Option, Scope, Stream } from "effect"
import { fileURLToPath } from "node:url"
import * as Subprocess from "../src/subprocess.ts"

// Deterministic seam tests against fixture children. Tests run under
// `bun --bun vitest`, so process.execPath is the pinned bun binary. All tests
// are it.live: they do real process I/O and need the real clock for kill
// escalation and timeouts.

const appServerRoot = fileURLToPath(new URL("..", import.meta.url))

const TestLayer = Subprocess.layer.pipe(Layer.provideMerge(BunServices.layer))

const fixture = (name: string): Subprocess.SpawnSpec => ({
  executable: process.execPath,
  args: [`test/fixtures/${name}.ts`],
  cwd: appServerRoot,
  // PATH is deliberately omitted: fixtures resolve via the absolute bun path.
  env: {},
})

const isAlive = (pid: number): boolean => {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

// The release finalizer awaits exit, but give the OS a moment to reap.
const expectDead = (pid: number) =>
  Effect.gen(function* () {
    for (let attempt = 0; attempt < 20 && isAlive(pid); attempt++) {
      yield* Effect.sleep("50 millis")
    }
    assert.strictEqual(isAlive(pid), false)
  })

describe("Subprocess", () => {
  it.live("captures stdout lines and observes a clean exit", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const child = yield* subprocess.spawn(fixture("echo-lines"))
      const lines = yield* Stream.runCollect(child.stdoutLines)
      assert.deepStrictEqual(lines, ["one", "two", "three"])
      assert.strictEqual(yield* child.exitCode, 0)
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("reports a non-zero exit code", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const child = yield* subprocess.spawn(fixture("exit-code"))
      assert.strictEqual(yield* child.exitCode, 7)
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("round-trips lines over stdin and stdout, then exits on EOF", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const child = yield* subprocess.spawn(fixture("line-echo"))
      yield* child.writeLine("ping")
      const first = yield* Stream.runHead(child.stdoutLines)
      assert.deepStrictEqual(first, Option.some("echo:ping"))
      yield* child.endInput
      assert.strictEqual(yield* child.exitCode, 0)
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("keeps a bounded, truncated stderr tail", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const child = yield* subprocess.spawn(fixture("stderr-flood"))
      assert.strictEqual(yield* child.exitCode, 0)
      const tail = yield* child.stderrTail
      assert.strictEqual(tail.length, 100)
      assert.strictEqual(tail[0], "line 401")
      const last = tail[99]!
      assert.strictEqual(last.startsWith("end "), true)
      assert.strictEqual(last.length, 2_001)
      assert.strictEqual(last.endsWith("…"), true)
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("fails to spawn a missing executable with a tagged error", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const result = yield* Effect.result(
        subprocess.spawn({ executable: "/nonexistent/definitely-not-here", env: {} }),
      )
      assert.strictEqual(result._tag, "Failure")
      if (result._tag === "Failure") {
        assert.strictEqual(result.failure._tag, "SubprocessError")
        assert.strictEqual(result.failure.operation, "spawn")
      }
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("terminates the child when the scope closes", () =>
    Effect.gen(function* () {
      const scope = yield* Scope.make()
      const child = yield* Effect.gen(function* () {
        const subprocess = yield* Subprocess.Subprocess
        return yield* subprocess.spawn(fixture("sleep-forever"))
      }).pipe(Scope.provide(scope))
      assert.strictEqual(isAlive(child.pid), true)
      yield* Scope.close(scope, Exit.void)
      yield* expectDead(child.pid)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("escalates to SIGKILL when the child ignores SIGTERM", () =>
    Effect.gen(function* () {
      const scope = yield* Scope.make()
      const child = yield* Effect.gen(function* () {
        const subprocess = yield* Subprocess.Subprocess
        return yield* subprocess.spawn({
          ...fixture("stubborn"),
          forceKillAfter: "200 millis",
        })
      }).pipe(Scope.provide(scope))
      // Wait until the SIGTERM handler is installed before terminating.
      assert.deepStrictEqual(yield* Stream.runHead(child.stdoutLines), Option.some("armed"))
      yield* Scope.close(scope, Exit.void)
      yield* expectDead(child.pid)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("kills the child when an operation times out", () =>
    Effect.gen(function* () {
      let pid = 0
      const result = yield* Effect.result(
        Effect.gen(function* () {
          const subprocess = yield* Subprocess.Subprocess
          const child = yield* subprocess.spawn(fixture("sleep-forever"))
          pid = child.pid
          // The child never writes, so waiting for output must time out.
          return yield* Stream.runHead(child.stdoutLines).pipe(Effect.timeout("250 millis"))
        }).pipe(Effect.scoped),
      )
      assert.strictEqual(result._tag, "Failure")
      yield* expectDead(pid)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("kills the child when the caller is interrupted", () =>
    Effect.gen(function* () {
      let pid = 0
      const started = yield* Deferred.make<void>()
      const fiber = yield* Effect.gen(function* () {
        const subprocess = yield* Subprocess.Subprocess
        const child = yield* subprocess.spawn(fixture("sleep-forever"))
        pid = child.pid
        yield* Deferred.succeed(started, void 0)
        yield* Effect.never
      }).pipe(Effect.scoped, Effect.forkChild)
      yield* Deferred.await(started)
      yield* Fiber.interrupt(fiber)
      yield* expectDead(pid)
    }).pipe(Effect.provide(TestLayer)),
  )
})
