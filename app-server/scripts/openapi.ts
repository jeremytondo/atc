import { fileURLToPath } from "node:url"
import { openApiJson } from "../src/openapi.ts"

// Maintains the checked-in OpenAPI artifact (app-server/openapi.json).
//
//   bun run scripts/openapi.ts           regenerate the artifact (--write)
//   bun run scripts/openapi.ts --check   fail (exit 1) if the artifact is stale
//   bun run scripts/openapi.ts --print   write the document to stdout (used by tests)
//
// Exposed as `mise run openapi` / `mise run openapi:check`.

const artifactPath = fileURLToPath(new URL("../openapi.json", import.meta.url))
const mode = Bun.argv[2] ?? "--write"

switch (mode) {
  case "--write": {
    await Bun.write(artifactPath, openApiJson)
    console.error(`wrote ${artifactPath}`)
    break
  }
  case "--check": {
    const checkedIn = await Bun.file(artifactPath)
      .text()
      .catch(() => "")
    if (checkedIn !== openApiJson) {
      console.error(
        "openapi.json is stale — run `mise run openapi`, review the diff, and commit the result",
      )
      process.exit(1)
    }
    break
  }
  case "--print": {
    process.stdout.write(openApiJson)
    break
  }
  default: {
    console.error(`unknown mode ${mode}; expected --write (default), --check, or --print`)
    process.exit(2)
  }
}
