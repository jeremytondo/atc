import { describe, expect, it } from "vitest"
import {
  ConfigError,
  DEFAULT_ENDPOINT,
  DEFAULT_ZMX_EXECUTABLE,
  defaultZmxDir,
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
      zmxExecutable: "/flag/zmx",
      zmxDir: "/flag/state/terminals",
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
      zmxExecutable: "/env/zmx",
      zmxDir: "/env/state/atc/terminals",
    })
    expect(resolve({}, { HOME: "/home/test" })).toMatchObject({
      zmxExecutable: DEFAULT_ZMX_EXECUTABLE,
      zmxDir: "/home/test/.local/state/atc/terminals",
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
    expect(resolve({ zmxExecutable: "./zmx" }, {})).toBeInstanceOf(ConfigError)
    expect(resolve({ zmxDir: "relative/zmx" }, {})).toBeInstanceOf(ConfigError)
  })
})

describe("defaultZmxDir", () => {
  it("follows the App Server's XDG state convention", () => {
    expect(defaultZmxDir({ XDG_STATE_HOME: "/state" }, "/fallback")).toBe("/state/atc/terminals")
    expect(defaultZmxDir({}, "/fallback")).toBe("/fallback/.local/state/atc/terminals")
  })
})
