import { it as effectIt } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Exit, Layer, Scope } from "effect"
import { existsSync } from "node:fs"
import { fileURLToPath } from "node:url"
import { describe, expect, it } from "vitest"
import * as Subprocess from "../../app-server/src/platform/subprocess.ts"
import * as Config from "../src/config.ts"
import * as Remote from "../src/remote.ts"

const fakeSsh = fileURLToPath(new URL("fixtures/fake-ssh-tunnel.ts", import.meta.url))
const SubprocessLayer = Subprocess.layer.pipe(Layer.provideMerge(BunServices.layer))

describe("remote App Server tunnel", () => {
  it("forwards one private Unix socket with bounded SSH liveness settings", () => {
    const connection: Config.RemoteConnection = {
      type: "remote",
      host: "workstation",
      sshExecutable: "/usr/bin/ssh",
      remoteAtcExecutable: ".local/bin/atc",
      remotePort: 8331,
      socketPath: "/tmp/atc-tui-test.sock",
    }
    expect(Remote.tunnelSpec(connection)).toStrictEqual({
      executable: "/usr/bin/ssh",
      args: [
        "-N",
        "-T",
        "-o",
        "ExitOnForwardFailure=yes",
        "-o",
        "ControlMaster=no",
        "-o",
        "ControlPath=none",
        "-o",
        "ForkAfterAuthentication=no",
        "-o",
        "StreamLocalBindUnlink=yes",
        "-o",
        "StreamLocalBindMask=0177",
        "-o",
        "ServerAliveInterval=5",
        "-o",
        "ServerAliveCountMax=2",
        "-o",
        "ConnectTimeout=5",
        "-L",
        "/tmp/atc-tui-test.sock:127.0.0.1:8331",
        "workstation",
      ],
    })
  })

  effectIt.live("removes the private socket when the owning scope closes", () => {
    const socketPath = `/tmp/atc-tui-test-${process.pid}-${crypto.randomUUID().slice(0, 8)}.sock`
    const config: Config.ClientConfig["Service"] = {
      endpoint: new URL("http://127.0.0.1:7331"),
      connection: {
        type: "remote",
        host: "workstation",
        sshExecutable: fakeSsh,
        remoteAtcExecutable: ".local/bin/atc",
        remotePort: 7331,
        socketPath,
      },
      environment: process.env,
    }

    return Effect.gen(function* () {
      const remote = yield* Remote.make
      const scope = yield* Scope.make()
      yield* remote.start.pipe(Scope.provide(scope), Effect.ensuring(Scope.close(scope, Exit.void)))
      expect(existsSync(socketPath)).toBe(false)
    }).pipe(Effect.provideService(Config.ClientConfig, config), Effect.provide(SubprocessLayer))
  })
})
