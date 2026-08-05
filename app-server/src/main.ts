import { BunRuntime, BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { Command } from "effect/unstable/cli"
import * as BuildInfo from "./buildInfo.ts"
import { fs, health, version } from "./cli/cli.ts"
import { api, capabilities, context } from "./cli/gateway.ts"
import { project } from "./cli/projects.ts"
import { serve } from "./cli/serve.ts"
import { terminal } from "./cli/terminals.ts"
import { smoke } from "./smoke.ts"
import * as Subprocess from "./subprocess.ts"

// The entrypoint: root command assembly plus the only runMain in the
// application. Everything else is Layers and Effects, so even the CLI
// version resolves through the injected BuildInfo service.
const atc = Command.make("atc").pipe(
  Command.withDescription("ATC App Server"),
  Command.withSubcommands([
    serve,
    api,
    context,
    capabilities,
    health,
    version,
    project,
    terminal,
    fs,
    smoke,
  ]),
)

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
