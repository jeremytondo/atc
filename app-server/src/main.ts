import { BunRuntime, BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import { Command } from "effect/unstable/cli"
import * as BuildInfo from "./platform/buildInfo.ts"
import { fs, health, version } from "./cli/cli.ts"
import { api, capabilities, context } from "./cli/gateway.ts"
import { project } from "./cli/projects.ts"
import { serve, start, status, stop } from "./cli/serve.ts"
import { service } from "./cli/service.ts"
import { terminal } from "./cli/terminals.ts"
import { token } from "./cli/token.ts"
import { thread } from "./cli/threads.ts"
import { upgrade } from "./cli/upgrade.ts"
import { smoke } from "./agents/smoke.ts"
import * as Subprocess from "./platform/subprocess.ts"

// The entrypoint: root command assembly plus the only runMain in the
// application. Everything else is Layers and Effects, so even the CLI
// version resolves through the injected BuildInfo service.
const atc = Command.make("atc").pipe(
  Command.withDescription("ATC App Server"),
  Command.withSubcommands([
    serve,
    start,
    stop,
    status,
    service,
    upgrade,
    api,
    context,
    capabilities,
    health,
    version,
    project,
    thread,
    terminal,
    token,
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
