// AGENTS.md invariant: everything reads AppConfig — never process.env. The
// approved boundary modules (config.ts itself, subprocess.ts's child-env
// builder, service.ts's sanctioned PATH read) are exempted via config
// overrides in .oxlintrc.json, not here, so the exemption list is visible in
// one place.
import { defineRule } from "@oxlint/plugins"

import { getPropertyName, isIdentifier, unwrapExpression } from "../utils.ts"

const isProcessObject = (node: unknown): boolean => {
  const expression = unwrapExpression(node)
  if (isIdentifier(expression, "process")) return true
  if (expression === undefined || expression.type !== "MemberExpression") return false
  const object = unwrapExpression(expression.object)
  return isIdentifier(object, "globalThis") && getPropertyName(expression.property) === "process"
}

export default defineRule({
  meta: {
    type: "problem",
    docs: {
      description:
        "Disallow process.env reads outside the approved configuration boundary modules.",
    },
  },
  createOnce(context) {
    return {
      MemberExpression(node) {
        if (getPropertyName(node.property) !== "env") return
        if (!isProcessObject(node.object)) return
        context.report({
          node,
          message:
            "Read configuration through AppConfig (src/platform/config.ts), never process.env; flags > environment > config file > defaults is settled in one place.",
        })
      },
    }
  },
})
