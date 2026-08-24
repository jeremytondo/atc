import { assert, describe, it } from "@effect/vitest"
import { Effect } from "effect"
import type * as AppServer from "../src/appServer.ts"
import type * as Config from "../src/config.ts"
import {
  attachEnvironment,
  attachRemote,
  localAttachSpec,
  remoteAttachSpec,
} from "../src/terminalAttachment.ts"

const terminal = {
  id: "terminal-1",
  sessionName: "atc-terminal-1",
} as AppServer.Terminal

const localConnection: Config.LocalConnection = {
  type: "local",
  zmxExecutable: "/usr/local/bin/zmx",
  zmxDir: "/state/atc/terminals",
}

const localConfig: Config.ClientConfig["Service"] = {
  endpoint: new URL("http://127.0.0.1:7331"),
  connection: localConnection,
  environment: {
    HOME: "/home/test",
    PATH: "/bin",
    ZMX_DIR: "/wrong",
    ZMX_SESSION: "outer",
    ZMX_SESSION_PREFIX: "prefix",
  },
}

const remoteConnection: Config.RemoteConnection = {
  type: "remote",
  host: "workstation",
  sshExecutable: "/usr/bin/ssh",
  remoteAtcExecutable: ".local/bin/atc",
  remotePort: 7331,
  socketPath: "/tmp/atc-tui-test.sock",
}

const remoteConfig: Config.ClientConfig["Service"] = {
  endpoint: new URL("http://127.0.0.1:7331"),
  connection: remoteConnection,
  environment: { HOME: "/home/test", PATH: "/bin" },
}

describe("terminal attachment", () => {
  it("uses the server-provided session name in the local ATC namespace", () => {
    assert.deepStrictEqual(localAttachSpec(localConfig, localConnection, terminal), {
      executable: "/usr/local/bin/zmx",
      args: ["attach", "atc-terminal-1"],
      env: {
        HOME: "/home/test",
        PATH: "/bin",
        ZMX_DIR: "/state/atc/terminals",
      },
    })
  })

  it("quotes one remote atc attach command and gives SSH the real TTY", () => {
    assert.deepStrictEqual(remoteAttachSpec(remoteConfig, remoteConnection, terminal), {
      executable: "/usr/bin/ssh",
      args: [
        "-tt",
        "-o",
        "ServerAliveInterval=5",
        "-o",
        "ServerAliveCountMax=2",
        "-o",
        "ConnectTimeout=5",
        "workstation",
        "'.local/bin/atc' 'terminal' 'attach' 'terminal-1'",
      ],
      env: { HOME: "/home/test", PATH: "/bin" },
    })
  })

  it.effect("reattaches the same remote terminal after SSH connection failures", () =>
    Effect.gen(function* () {
      const exits = [255, 255, 0]
      const seen: Array<string> = []
      const runner = (spec: { readonly args: ReadonlyArray<string> }) =>
        Effect.sync(() => {
          seen.push(spec.args.at(-1) ?? "")
          return { exitCode: exits.shift() ?? 0, signalCode: null }
        })
      yield* attachRemote(remoteConfig, remoteConnection, terminal, {
        runner,
        wait: () => Effect.void,
        onReconnect: () => Effect.void,
      })
      assert.deepStrictEqual(seen, [
        "'.local/bin/atc' 'terminal' 'attach' 'terminal-1'",
        "'.local/bin/atc' 'terminal' 'attach' 'terminal-1'",
        "'.local/bin/atc' 'terminal' 'attach' 'terminal-1'",
      ])
    }),
  )

  it("scrubs nested-client markers without dropping the caller environment", () => {
    assert.deepStrictEqual(
      attachEnvironment(
        {
          TERM: "xterm-256color",
          EMPTY: undefined,
          ZMX_SESSION: "manager",
          ZMX_SESSION_PREFIX: "nested",
        },
        "/private/atc",
      ),
      {
        TERM: "xterm-256color",
        ZMX_DIR: "/private/atc",
      },
    )
  })
})
