import { describe, expect, it } from "vitest"
import {
  ConfigError,
  DEFAULT_ENDPOINT,
  DEFAULT_ZMX_EXECUTABLE,
  defaultZmxDir,
  makeRemoteSocketPath,
  resolve,
} from "../src/config.ts"

describe("resolve", () => {
  it("uses flag, environment, and default endpoint precedence", () => {
    expect(
      resolve({ endpoint: "https://flag.example" }, { ATC_ENDPOINT: "https://env.example" }),
    ).toMatchObject({ endpoint: new URL("https://flag.example") })
    expect(resolve({}, { ATC_ENDPOINT: "https://env.example" })).toMatchObject({
      endpoint: new URL("https://env.example"),
    })
    expect(resolve({}, {})).toMatchObject({ endpoint: new URL(DEFAULT_ENDPOINT) })
  })

  it("uses explicit, environment, and default local zmx settings", () => {
    expect(
      resolve(
        { zmxExecutable: "/flag/zmx", zmxDir: "/flag/state/terminals" },
        {
          ATC_ZMX_EXECUTABLE: "/env/zmx",
          XDG_STATE_HOME: "/env/state",
        },
      ),
    ).toMatchObject({
      connection: {
        type: "local",
        zmxExecutable: "/flag/zmx",
        zmxDir: "/flag/state/terminals",
      },
    })
    expect(
      resolve(
        {},
        {
          ATC_ZMX_EXECUTABLE: "/env/zmx",
          XDG_STATE_HOME: "/env/state",
        },
      ),
    ).toMatchObject({
      connection: {
        type: "local",
        zmxExecutable: "/env/zmx",
        zmxDir: "/env/state/atc/terminals",
      },
    })
    expect(resolve({}, { HOME: "/home/test" })).toMatchObject({
      connection: {
        type: "local",
        zmxExecutable: DEFAULT_ZMX_EXECUTABLE,
        zmxDir: "/home/test/.local/state/atc/terminals",
      },
    })
  })

  it("derives a private local endpoint and SSH settings for remote mode", () => {
    expect(
      resolve(
        {
          remote: "dev@workstation",
          sshExecutable: "/usr/bin/ssh",
          remoteAtcExecutable: "/opt/atc/bin/atc",
          remotePort: 8331,
        },
        { ATC_ENDPOINT: "https://ignored.example", ATC_TOKEN: "ignored" },
        "/tmp/atc-tui-test.sock",
      ),
    ).toStrictEqual({
      endpoint: new URL("http://127.0.0.1:8331"),
      connection: {
        type: "remote",
        host: "dev@workstation",
        sshExecutable: "/usr/bin/ssh",
        remoteAtcExecutable: "/opt/atc/bin/atc",
        remotePort: 8331,
        socketPath: "/tmp/atc-tui-test.sock",
      },
      environment: { ATC_ENDPOINT: "https://ignored.example", ATC_TOKEN: "ignored" },
    })
  })

  it("keeps bearer credentials separate from the URL", () => {
    expect(
      resolve({ endpoint: "https://server.example" }, { ATC_TOKEN: " secret " }),
    ).toMatchObject({
      token: "secret",
    })
    expect(resolve({ endpoint: "https://user:pass@server.example" }, {})).toBeInstanceOf(
      ConfigError,
    )
  })

  it("rejects invalid endpoints and local zmx paths", () => {
    expect(resolve({ endpoint: "ssh://server.example" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ endpoint: "https://server.example/atc" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ endpoint: "https://server.example?mode=tui" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ endpoint: "https://server.example#events" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ zmxExecutable: "./zmx" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ zmxDir: "relative/zmx" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ remote: "-oProxyCommand=bad" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ remote: "two hosts" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ remote: "host", sshExecutable: "./ssh" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ remote: "host", remotePort: 0 }, {})).toBeInstanceOf(ConfigError)
  })
})

describe("makeRemoteSocketPath", () => {
  it("uses a short absolute per-process path suitable for macOS Unix sockets", () => {
    expect(makeRemoteSocketPath(42, "abcdef12")).toBe("/tmp/atc-tui-42-abcdef12.sock")
    expect(makeRemoteSocketPath(42, "abcdef12").length).toBeLessThan(100)
  })
})

describe("defaultZmxDir", () => {
  it("follows the App Server's XDG state convention", () => {
    expect(defaultZmxDir({ XDG_STATE_HOME: "/state" }, "/fallback")).toBe("/state/atc/terminals")
    expect(defaultZmxDir({}, "/fallback")).toBe("/fallback/.local/state/atc/terminals")
  })
})
