import { Context, Layer } from "effect"
import packageJson from "../package.json"

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
  // Real values arrive with the standalone-executable build, which will
  // inject them at compile time; running from source uses placeholders.
  commit: "dev",
  builtAt: "dev",
}

export const layer = Layer.succeed(BuildInfo)(buildInfo)
