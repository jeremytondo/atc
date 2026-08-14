// AGENTS.md invariant: exactly one runtime entrypoint — BunRuntime.runMain in
// src/main.ts (exempted via config override in .oxlintrc.json). Everywhere
// else, including tests, Effects stay lazy values: @effect/vitest's it.effect
// manages the runtime in tests, and hand-run Effects escape structured
// concurrency and scope-based resource release.
import { defineRule } from "@oxlint/plugins"

import { getImportSource, getPropertyName, unwrapExpression } from "../utils.ts"

// Namespace-like bindings whose members are runtime executors, keyed by the
// member-name test applied to them.
const RUN_MEMBER_SOURCES: ReadonlyArray<{
  readonly module: string
  readonly exported: string
  readonly isRunMember: (property: string) => boolean
}> = [
  { module: "effect", exported: "Effect", isRunMember: (p) => /^run[A-Z]/.test(p) },
  { module: "effect", exported: "ManagedRuntime", isRunMember: (p) => p === "make" },
  { module: "@effect/platform-bun", exported: "BunRuntime", isRunMember: (p) => p === "runMain" },
]

const NAMESPACE_MODULES: ReadonlyMap<string, (property: string) => boolean> = new Map([
  ["effect/Effect", (p: string) => /^run[A-Z]/.test(p)],
  ["effect/ManagedRuntime", (p: string) => p === "make"],
  ["@effect/platform-bun/BunRuntime", (p: string) => p === "runMain"],
])

const MESSAGE =
  "Exactly one runtime entrypoint: BunRuntime.runMain in src/main.ts. Tests use @effect/vitest (it.effect); nothing else hand-runs Effects."

export default defineRule({
  meta: {
    type: "problem",
    docs: {
      description: "Disallow manual Effect runtimes outside the src/main.ts entrypoint.",
    },
  },
  createOnce(context) {
    // Local binding name -> predicate deciding whether a member access on it
    // is a runtime executor.
    const runtimeBindings = new Map<string, (property: string) => boolean>()
    // Local binding names of directly imported executors, e.g.
    // `import { runPromise } from "effect/Effect"`.
    const directExecutors = new Set<string>()

    return {
      before() {
        runtimeBindings.clear()
        directExecutors.clear()
      },
      ImportDeclaration(node) {
        const source = getImportSource(node)
        if (source === undefined) return

        for (const specifier of node.specifiers) {
          if (specifier.type === "ImportNamespaceSpecifier") {
            const isRunMember = NAMESPACE_MODULES.get(source)
            if (isRunMember !== undefined) runtimeBindings.set(specifier.local.name, isRunMember)
            continue
          }
          if (specifier.type !== "ImportSpecifier") continue
          const imported = getPropertyName(specifier.imported)
          if (imported === undefined) continue
          for (const entry of RUN_MEMBER_SOURCES) {
            if (source === entry.module && imported === entry.exported) {
              runtimeBindings.set(specifier.local.name, entry.isRunMember)
            }
          }
          // A named import of an executor itself: the module's member test
          // applies to the imported name, and calls go through the local one.
          const isRunMember = NAMESPACE_MODULES.get(source)
          if (isRunMember !== undefined && isRunMember(imported)) {
            directExecutors.add(specifier.local.name)
          }
        }
      },
      CallExpression(node) {
        const callee = unwrapExpression(node.callee)
        if (callee === undefined || callee.type !== "Identifier") return
        if (!directExecutors.has(callee.name)) return
        context.report({ node, message: MESSAGE })
      },
      MemberExpression(node) {
        const object = unwrapExpression(node.object)
        if (object === undefined || object.type !== "Identifier") return
        const isRunMember = runtimeBindings.get(object.name)
        if (isRunMember === undefined) return

        const property = getPropertyName(node.property)
        if (property === undefined || !isRunMember(property)) return

        context.report({ node, message: MESSAGE })
      },
    }
  },
})
