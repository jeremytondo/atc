import { BunRuntime, BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { Command } from "effect/unstable/cli"
import * as BuildInfo from "./buildInfo.ts"
import { atc } from "./cli.ts"
import * as Subprocess from "./subprocess.ts"

// The only runMain in the application. Everything else is Layers and Effects,
// so even the CLI version resolves through the injected BuildInfo service.
Effect.gen(function* () {
  const build = yield* BuildInfo.BuildInfo
  yield* Command.run(atc, { version: build.version })
}).pipe(
  Effect.provide(
    Layer.mergeAll(
      BuildInfo.layer,
      BunServices.layer,
      Subprocess.layer.pipe(Layer.provide(BunServices.layer)),
    ),
  ),
  BunRuntime.runMain,
)
