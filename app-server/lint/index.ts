// ATC's project-owned oxlint plugin: the AGENTS.md invariants that a grep
// can't enforce, as tested lint rules. Wired up via `jsPlugins` in
// .oxlintrc.json; per-file exemptions (the approved boundary modules) live in
// that config's overrides, never inside the rules. Tests: test/lint/.
import { definePlugin } from "@oxlint/plugins"

import canonicalNamespaceImports from "./rules/canonical-namespace-imports.ts"
import noAdhocSpawn from "./rules/no-adhoc-spawn.ts"
import noManualEffectRuntime from "./rules/no-manual-effect-runtime.ts"
import noProcessEnv from "./rules/no-process-env.ts"

export default definePlugin({
  meta: {
    name: "atc",
  },
  rules: {
    "canonical-namespace-imports": canonicalNamespaceImports,
    "no-adhoc-spawn": noAdhocSpawn,
    "no-manual-effect-runtime": noManualEffectRuntime,
    "no-process-env": noProcessEnv,
  },
})
