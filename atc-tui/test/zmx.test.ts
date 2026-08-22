import { describe, expect, it } from "vitest"
import { attachEnvironment, attachSpec } from "../src/zmx.ts"

describe("direct zmx attachment", () => {
  it("uses the server-provided session name in the ATC namespace", () => {
    expect(
      attachSpec(
        {
          zmxExecutable: "/usr/local/bin/zmx",
          zmxDir: "/state/atc/terminals",
          environment: {
            HOME: "/home/test",
            PATH: "/bin",
            ZMX_DIR: "/wrong",
            ZMX_SESSION: "outer",
            ZMX_SESSION_PREFIX: "prefix",
          },
        },
        { sessionName: "atc-1234" },
      ),
    ).toStrictEqual({
      executable: "/usr/local/bin/zmx",
      args: ["attach", "atc-1234"],
      env: {
        HOME: "/home/test",
        PATH: "/bin",
        ZMX_DIR: "/state/atc/terminals",
      },
    })
  })

  it("scrubs nested-client markers without dropping the caller environment", () => {
    expect(
      attachEnvironment(
        {
          TERM: "xterm-256color",
          EMPTY: undefined,
          ZMX_SESSION: "manager",
          ZMX_SESSION_PREFIX: "nested",
        },
        "/private/atc",
      ),
    ).toEqual({
      TERM: "xterm-256color",
      ZMX_DIR: "/private/atc",
    })
  })
})
