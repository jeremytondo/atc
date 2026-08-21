import { describe, expect, it } from "vitest"
import { ConfigError, DEFAULT_ENDPOINT, resolve } from "../src/config.ts"

describe("resolve", () => {
  it("uses flag, environment, and default precedence", () => {
    expect(resolve("https://flag.example", { ATC_ENDPOINT: "https://env.example" })).toMatchObject({
      endpoint: new URL("https://flag.example"),
    })
    expect(resolve(undefined, { ATC_ENDPOINT: "https://env.example" })).toMatchObject({
      endpoint: new URL("https://env.example"),
    })
    expect(resolve(undefined, {})).toMatchObject({ endpoint: new URL(DEFAULT_ENDPOINT) })
  })

  it("keeps bearer credentials separate from the URL", () => {
    expect(resolve("https://server.example", { ATC_TOKEN: " secret " })).toMatchObject({
      token: "secret",
    })
    expect(resolve("https://user:pass@server.example", {})).toBeInstanceOf(ConfigError)
  })

  it("rejects non-http endpoints", () => {
    expect(resolve("ssh://server.example", {})).toBeInstanceOf(ConfigError)
  })
})
