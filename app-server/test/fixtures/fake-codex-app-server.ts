// Fixture stand-in for `codex app-server --stdio`: answers initialize over
// newline-delimited JSON-RPC and exits cleanly on stdin EOF. Pass "error" as
// argv[2] to answer initialize with a JSON-RPC error instead.
import { createInterface } from "node:readline"

const mode = process.argv[2] ?? "ok"
const lines = createInterface({ input: process.stdin })
lines.on("line", (line) => {
  const message = JSON.parse(line) as { id?: number; method?: string }
  if (message.method === "initialize" && message.id !== undefined) {
    console.log(
      JSON.stringify(
        mode === "error"
          ? { id: message.id, error: { code: -32600, message: "fixture rejects initialize" } }
          : { id: message.id, result: { userAgent: "fake-codex-app-server" } },
      ),
    )
  }
})
lines.on("close", () => process.exit(0))
