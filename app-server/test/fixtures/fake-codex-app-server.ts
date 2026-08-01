// Fixture stand-in for `codex app-server --stdio`: answers initialize over
// newline-delimited JSON-RPC and exits cleanly on stdin EOF. argv[2] selects
// a failure mode: "error" answers with a JSON-RPC error, "empty" answers
// with neither result nor error, "exit-nonzero" answers ok but exits 3.
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
          : mode === "empty"
            ? { id: message.id }
            : { id: message.id, result: { userAgent: "fake-codex-app-server" } },
      ),
    )
  }
})
lines.on("close", () => process.exit(mode === "exit-nonzero" ? 3 : 0))
