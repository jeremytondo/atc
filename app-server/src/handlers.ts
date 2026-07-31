import { Effect } from "effect"
import { HttpApiBuilder } from "effect/unstable/httpapi"
import { Api } from "./api.ts"
import { BuildInfo } from "./buildInfo.ts"

/** Implements the /api/v1 contract. App construction only — no listener. */
export const V1Handlers = HttpApiBuilder.group(
  Api,
  "v1",
  Effect.fnUntraced(function* (handlers) {
    const build = yield* BuildInfo
    return handlers
      .handle("health", () => Effect.succeed({ status: "ok" } as const))
      .handle("version", () =>
        Effect.succeed({
          version: build.version,
          apiVersion: "v1",
          commit: build.commit,
          builtAt: build.builtAt,
        } as const),
      )
  }),
)
