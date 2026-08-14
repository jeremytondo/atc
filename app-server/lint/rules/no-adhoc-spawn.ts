// AGENTS.md invariant: all child processes go through the Subprocess service
// (src/platform/subprocess.ts) so kill+reap and PTY guarantees hold. That
// module, tests, fixtures, and build scripts are exempted via config
// overrides in .oxlintrc.json.
import { defineRule } from "@oxlint/plugins"

import { getImportSource, getPropertyName, isIdentifier, unwrapExpression } from "../utils.ts"

const SPAWN_PROPERTIES = new Set(["spawn", "spawnSync"])
const CHILD_PROCESS_MODULES = new Set(["node:child_process", "child_process"])

const isBunObject = (node: unknown): boolean => {
  const expression = unwrapExpression(node)
  if (isIdentifier(expression, "Bun")) return true
  if (expression === undefined || expression.type !== "MemberExpression") return false
  const object = unwrapExpression(expression.object)
  return isIdentifier(object, "globalThis") && getPropertyName(expression.property) === "Bun"
}

export default defineRule({
  meta: {
    type: "problem",
    docs: {
      description: "Disallow spawning child processes outside the Subprocess service.",
    },
  },
  createOnce(context) {
    const message =
      "Spawn child processes through the Subprocess service (src/platform/subprocess.ts); ad-hoc spawns escape its scoped kill+reap guarantees."
    return {
      MemberExpression(node) {
        const property = getPropertyName(node.property)
        if (property === undefined || !SPAWN_PROPERTIES.has(property)) return
        if (!isBunObject(node.object)) return
        context.report({ node, message })
      },
      ImportDeclaration(node) {
        const source = getImportSource(node)
        if (source === undefined || !CHILD_PROCESS_MODULES.has(source)) return
        context.report({ node, message })
      },
    }
  },
})
