import { Context, Layer } from "effect"
import packageJson from "../../package.json"

// Compile-time constants injected by scripts/build.ts via `bun build --define`.
// Running from source leaves them undefined, so dev placeholders apply — and
// a release build stamps its version over the package.json placeholder.
declare const ATC_BUILD_COMMIT: string | undefined
declare const ATC_BUILD_BUILT_AT: string | undefined
declare const ATC_BUILD_VERSION: string | undefined
declare const ATC_BUILD_CHANNEL: string | undefined

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
    /** Release channel ("stable" | "dev") stamped by the release pipeline;
     * undefined for source runs and local builds, which have no channel to
     * upgrade against. */
    readonly channel: string | undefined
  }
>()("app-server/BuildInfo") {}

export const buildInfo: BuildInfo["Service"] = {
  version: typeof ATC_BUILD_VERSION === "string" ? ATC_BUILD_VERSION : packageJson.version,
  commit: typeof ATC_BUILD_COMMIT === "string" ? ATC_BUILD_COMMIT : "dev",
  builtAt: typeof ATC_BUILD_BUILT_AT === "string" ? ATC_BUILD_BUILT_AT : "dev",
  channel: typeof ATC_BUILD_CHANNEL === "string" ? ATC_BUILD_CHANNEL : undefined,
}

export const layer = Layer.succeed(BuildInfo)(buildInfo)
