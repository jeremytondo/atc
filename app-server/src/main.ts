import { BunRuntime, BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { Command } from "effect/unstable/cli"
import * as BuildInfo from "./buildInfo.ts"
import { atc } from "./cli.ts"

// The only runMain in the application. Everything else is Layers and Effects.
// Command.run needs a plain string before any Layer exists, so it reads the
// buildInfo constant directly; everything downstream uses the injected service.
Command.run(atc, { version: BuildInfo.buildInfo.version }).pipe(
  Effect.provide(Layer.mergeAll(BuildInfo.layer, BunServices.layer)),
  BunRuntime.runMain,
)
