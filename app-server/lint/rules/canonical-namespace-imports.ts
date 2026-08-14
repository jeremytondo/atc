// AGENTS.md invariant: own modules are referenced by their canonical name so
// one grep finds every call site — `import * as Terminals from
// ".../terminals.ts"`, never an alias. Named imports of a module's exports
// (service classes, commands, helpers) are fine because they already carry
// the exported name; what this rule bans is renaming: a namespace import
// that isn't the module's canonical name, or a named specifier imported
// `as` something else.
import { defineRule } from "@oxlint/plugins"

import { getImportSource, getPropertyName } from "../utils.ts"

// Canonical names that are not simply the capitalized file basename.
const CANONICAL_EXCEPTIONS: ReadonlyMap<string, string> = new Map([["zmxAdapter", "Zmx"]])

const canonicalName = (source: string): string | undefined => {
  const basename = source.split("/").at(-1)?.replace(/\.ts$/, "")
  if (basename === undefined || basename.length === 0) return undefined
  const exception = CANONICAL_EXCEPTIONS.get(basename)
  if (exception !== undefined) return exception
  // PascalCase across separator characters so hyphenated basenames still
  // yield a valid identifier (no-adhoc-spawn.ts -> NoAdhocSpawn).
  const name = basename
    .split(/[-_.]/)
    .filter((segment) => segment.length > 0)
    .map((segment) => segment[0]!.toUpperCase() + segment.slice(1))
    .join("")
  return name.length > 0 ? name : undefined
}

export default defineRule({
  meta: {
    type: "problem",
    docs: {
      description: "Require canonical, unaliased names when importing the project's own modules.",
    },
  },
  createOnce(context) {
    return {
      ImportDeclaration(node) {
        const source = getImportSource(node)
        if (source === undefined || !source.startsWith(".")) return

        for (const specifier of node.specifiers) {
          if (specifier.type === "ImportNamespaceSpecifier") {
            const expected = canonicalName(source)
            if (expected === undefined || specifier.local.name === expected) continue
            context.report({
              node: specifier,
              message: `Import ${source} under its canonical name \`${expected}\` so one grep finds every call site.`,
            })
            continue
          }
          if (specifier.type !== "ImportSpecifier") continue
          const imported = getPropertyName(specifier.imported)
          if (imported === undefined || imported === specifier.local.name) continue
          context.report({
            node: specifier,
            message: `Never alias an import: \`${imported} as ${specifier.local.name}\` hides the exported name from grep. Use a canonical namespace import instead.`,
          })
        }
      },
    }
  },
})
