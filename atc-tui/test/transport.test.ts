import { it as effectIt } from "@effect/vitest"
import { BunHttpClient } from "@effect/platform-bun"
import { Effect } from "effect"
import { HttpClient } from "effect/unstable/http"
import { unlink } from "node:fs/promises"
import { describe, expect, it } from "vitest"
import type * as Config from "../src/config.ts"
import * as Transport from "../src/transport.ts"

const local: Config.ClientConfig["Service"] = {
  endpoint: new URL("http://127.0.0.1:7331"),
  connection: { type: "local", zmxExecutable: "zmx", zmxDir: "/tmp/atc/terminals" },
  environment: {},
}

const remoteConnection: Config.RemoteConnection = {
  type: "remote",
  host: "workstation",
  sshExecutable: "ssh",
  remoteAtcExecutable: ".local/bin/atc",
  remotePort: 7331,
  socketPath: "/tmp/atc-tui-test.sock",
}

const remote: Config.ClientConfig["Service"] = {
  endpoint: new URL("http://127.0.0.1:7331"),
  connection: remoteConnection,
  environment: {},
}

describe("HTTP transport", () => {
  it("uses normal fetch locally and the private SSH socket remotely", () => {
    expect(Transport.fetchOptions(local)).toStrictEqual({})
    expect(Transport.fetchOptions(remote)).toStrictEqual({ unix: "/tmp/atc-tui-test.sock" })
  })

  effectIt.live("routes the Effect HTTP client through the configured Unix socket", () => {
    const socketPath = `/tmp/atc-tui-fetch-${process.pid}-${crypto.randomUUID().slice(0, 8)}.sock`
    const remoteConfig: Config.ClientConfig["Service"] = {
      ...remote,
      connection: { ...remoteConnection, socketPath },
    }
    return Effect.scoped(
      Effect.gen(function* () {
        yield* Effect.acquireRelease(
          Effect.sync(() =>
            Bun.serve({
              unix: socketPath,
              fetch: () => Response.json({ status: "ok" }),
            }),
          ),
          (server) =>
            Effect.promise(() => server.stop(true)).pipe(
              Effect.andThen(Effect.tryPromise(() => unlink(socketPath)).pipe(Effect.ignore)),
            ),
        )
        const client = yield* HttpClient.HttpClient
        const response = yield* Transport.provideFetchOptions(
          client.get(remoteConfig.endpoint),
          remoteConfig,
        )
        expect(response.status).toBe(200)
      }),
    ).pipe(Effect.provide(BunHttpClient.layer))
  })
})
