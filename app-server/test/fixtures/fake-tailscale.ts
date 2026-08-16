import * as fs from "node:fs"
import * as path from "node:path"

// Deterministic tailscale CLI stand-in. Wrapper-provided environment supplies
// defaults; files in FAKE_TAILSCALE_STATE let a running test change backend or
// serve behavior without mutating process.env:
//
//   backend     BackendState returned by `status --json`
//   serve-mode  "exit" makes the foreground serve child fail

const stateDir = process.env["FAKE_TAILSCALE_STATE"]
if (stateDir === undefined) {
  console.error("fake-tailscale: FAKE_TAILSCALE_STATE is not set")
  process.exit(2)
}
if (process.env["TAILSCALE_BE_CLI"] !== "1") {
  console.error("fake-tailscale: TAILSCALE_BE_CLI is not set to 1")
  process.exit(2)
}
fs.mkdirSync(stateDir, { recursive: true })

const setting = (name: string, fallback: string): string => {
  const file = path.join(stateDir, name)
  return fs.existsSync(file) ? fs.readFileSync(file, "utf8").trim() : fallback
}

const [command = "", ...args] = process.argv.slice(2)
const dnsName = process.env["FAKE_TAILSCALE_DNSNAME"] ?? "node.tailnet.ts.net."

switch (command) {
  case "status": {
    if (!args.includes("--json")) process.exit(2)
    console.log(
      JSON.stringify({
        BackendState: setting("backend", process.env["FAKE_TAILSCALE_BACKEND"] ?? "Running"),
        Self: { DNSName: dnsName },
      }),
    )
    process.exit(0)
  }
  case "serve": {
    const https = args.find((arg) => arg.startsWith("--https="))
    const port = https?.slice("--https=".length)
    if (port === undefined || args[1] !== `localhost:${port}`) process.exit(2)
    if (setting("serve-mode", process.env["FAKE_TAILSCALE_SERVE_MODE"] ?? "") === "exit") {
      console.error("fake-tailscale: serve unavailable")
      process.exit(1)
    }
    console.log(`Available within your tailnet: https://${dnsName.replace(/\.$/, "")}:${port}`)
    setInterval(() => {
      if (setting("serve-mode", "") === "exit") {
        console.error("fake-tailscale: serve exited")
        process.exit(1)
      }
    }, 20)
    break
  }
  default: {
    console.error(`fake-tailscale: unknown command "${command}"`)
    process.exit(2)
  }
}
