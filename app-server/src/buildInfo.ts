import { Context, Layer } from "effect"
import packageJson from "../package.json"

// Injected by the standalone-executable build (`bun build --define`, later
// work). When running from source they are undefined and fall back to dev
// placeholders.
declare const ATC_BUILD_COMMIT: string | undefined
declare const ATC_BUILD_TIME: string | undefined

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
  builtAt: typeof ATC_BUILD_TIME === "string" ? ATC_BUILD_TIME : "dev",
}

export const layer = Layer.succeed(BuildInfo)(buildInfo)
