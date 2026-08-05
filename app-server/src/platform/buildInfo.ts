import { Context, Layer } from "effect"
import packageJson from "../../package.json"

// Compile-time constants injected by scripts/build.ts via `bun build --define`.
// Running from source leaves them undefined, so dev placeholders apply.
declare const ATC_BUILD_COMMIT: string | undefined
declare const ATC_BUILD_BUILT_AT: string | undefined

/**
 * Build metadata for the running executable. Handlers and the CLI read this
 * service instead of probing the environment themselves.
 */
export class BuildInfo extends Context.Service<
  BuildInfo,
  {
    readonly version: string
    readonly commit: string
    readonly builtAt: string
  }
>()("app-server/BuildInfo") {}

export const buildInfo: BuildInfo["Service"] = {
  version: packageJson.version,
  commit: typeof ATC_BUILD_COMMIT === "string" ? ATC_BUILD_COMMIT : "dev",
  builtAt: typeof ATC_BUILD_BUILT_AT === "string" ? ATC_BUILD_BUILT_AT : "dev",
}

export const layer = Layer.succeed(BuildInfo)(buildInfo)
