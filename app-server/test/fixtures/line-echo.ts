// Fixture child: echoes each stdin line back on stdout, exits cleanly at EOF.
import { createInterface } from "node:readline"

const lines = createInterface({ input: process.stdin })
lines.on("line", (line) => console.log(`echo:${line}`))
lines.on("close", () => process.exit(0))
