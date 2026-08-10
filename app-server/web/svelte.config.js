import { readFileSync } from "node:fs"
import adapter from "@sveltejs/adapter-static"
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte"

const appServerVersion = JSON.parse(
  readFileSync(new URL("../package.json", import.meta.url), "utf-8"),
).version

// Fully prerendered static build (no SPA fallback): the UI is two fixed
// routes, so every page exists as a real HTML file the server can map
// directly. See src/routes/+layout.ts for the prerender switch.
/** @type {import("@sveltejs/kit").Config} */
export default {
  preprocess: vitePreprocess(),
  kit: {
    adapter: adapter(),
    // The default version is a build timestamp, which would dirty the
    // committed build output on every rebuild. Pinned to the package version
    // instead — release versions are stamped into the binary at compile time
    // (scripts/build.ts --version), not here, so this only changes when the
    // committed UI is rebuilt against a bumped package version. That leaves
    // SvelteKit's stale-deploy reload effectively disabled, which is fine
    // for two no-cache HTML routes over content-hashed chunks.
    version: { name: appServerVersion },
  },
}
