import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Exit, Layer, Scope, Stream } from "effect"
import * as Subprocess from "../../src/platform/subprocess.ts"
import { appServerRoot } from "../blackbox.ts"
import { collectText, waitForText } from "../testLayers.ts"

// The PTY variant of the Subprocess seam against the pty-report fixture.
// All tests are it.live: real processes, real pseudo-terminals, real clock.
// Note the PTY runs in canonical mode, so input reaches the child on "\n".

const TestLayer = Subprocess.layer.pipe(Layer.provideMerge(BunServices.layer))

const fixture: Subprocess.PtySpawnSpec = {
  executable: process.execPath,
  args: ["test/fixtures/pty-report.ts"],
  cwd: appServerRoot,
  env: {},
}

describe("Subprocess.spawnPty", () => {
  it.live("gives the child a real terminal and round-trips bytes", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const child = yield* subprocess.spawnPty({ ...fixture, captureOutputTail: true })
      const sink = yield* collectText(child.output)
      yield* waitForText(sink, "tty:true")
      yield* child.write("ping\n")
      yield* waitForText(sink, "in:ping")
      // The opt-in diagnostic tail sees the same bytes as the stream.
      assert.include(yield* child.outputTail, "in:ping")
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("reports the exit code when the child exits by itself", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const child = yield* subprocess.spawnPty(fixture)
      const sink = yield* collectText(child.output)
      yield* waitForText(sink, "tty:true")
      yield* child.write("q\n")
      assert.strictEqual(yield* child.exitCode, 7)
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("resizes the PTY and the child observes the window change", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const child = yield* subprocess.spawnPty({ ...fixture, cols: 80, rows: 24 })
      const sink = yield* collectText(child.output)
      yield* waitForText(sink, "tty:true")
      yield* child.resize({ cols: 132, rows: 43 })
      yield* waitForText(sink, "resize:132x43")
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("terminates the child when the scope closes", () =>
    Effect.gen(function* () {
      const scope = yield* Scope.make()
      const child = yield* Effect.gen(function* () {
        const subprocess = yield* Subprocess.Subprocess
        return yield* subprocess.spawnPty(fixture)
      }).pipe(Scope.provide(scope))
      assert.strictEqual(Subprocess.isProcessAlive(child.pid), true)
      yield* Scope.close(scope, Exit.void)
      assert.strictEqual(yield* Subprocess.waitForProcessExit(child.pid), true)
    }).pipe(Effect.provide(TestLayer)),
  )

  it.live("ends the output stream when the child exits", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const child = yield* subprocess.spawnPty(fixture)
      yield* child.write("q\n")
      // Draining to completion proves the stream ends rather than hanging.
      yield* Stream.runDrain(child.output).pipe(Effect.timeout("5 seconds"))
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("fails to spawn a missing executable with a tagged error", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const failure = yield* Effect.flip(
        subprocess.spawnPty({ executable: "/nonexistent/definitely-not-here", env: {} }),
      )
      assert.strictEqual(failure._tag, "SubprocessError")
      assert.strictEqual(failure.operation, "spawn")
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )

  it.live("fails writes after the terminal closes instead of dropping them", () =>
    Effect.gen(function* () {
      const subprocess = yield* Subprocess.Subprocess
      const child = yield* subprocess.spawnPty(fixture)
      yield* child.write("q\n")
      assert.strictEqual(yield* child.exitCode, 7)
      for (let attempt = 0; ; attempt++) {
        const result = yield* Effect.result(child.write("after-death"))
        if (result._tag === "Failure") {
          assert.strictEqual(result.failure.operation, "stdin")
          break
        }
        assert.isBelow(attempt, 50, "write kept succeeding after child exit")
        yield* Effect.sleep("20 millis")
      }
    }).pipe(Effect.scoped, Effect.provide(TestLayer)),
  )
})
