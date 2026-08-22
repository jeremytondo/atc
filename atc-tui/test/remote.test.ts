import { describe, expect, it } from "vitest"
import type * as Config from "../src/config.ts"
import { tunnelSpec } from "../src/remote.ts"

describe("remote App Server tunnel", () => {
  it("forwards one private local port with bounded SSH liveness settings", () => {
    const connection: Config.RemoteConnection = {
      type: "remote",
      host: "workstation",
      sshExecutable: "/usr/bin/ssh",
      remoteAtcExecutable: ".local/bin/atc",
      remotePort: 8331,
      tunnelPort: 18331,
    }
    expect(tunnelSpec(connection)).toStrictEqual({
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
        "ServerAliveInterval=5",
        "-o",
        "ServerAliveCountMax=2",
        "-o",
        "ConnectTimeout=5",
        "-L",
        "127.0.0.1:18331:127.0.0.1:8331",
        "workstation",
      ],
    })
  })
})
