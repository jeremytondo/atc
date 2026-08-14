// Shared AST helpers for the ATC oxlint plugin. Rules receive ESTree nodes
// typed loosely by oxlint, so these narrow `unknown` into the shapes rules
// care about. The plugin is lint tooling that runs inside oxlint's runtime,
// not server code, so it deliberately stays plain TypeScript (no Effect).
import type { ESTree } from "@oxlint/plugins"

type AstNode = ESTree.Node

type ExpressionWrapper =
  | ESTree.ChainExpression
  | ESTree.ParenthesizedExpression
  | ESTree.TSNonNullExpression
  | ESTree.TSAsExpression
  | ESTree.TSTypeAssertion

export const asAstNode = (node: unknown): AstNode | undefined =>
  typeof node === "object" && node !== null && "type" in node && typeof node.type === "string"
    ? (node as AstNode)
    : undefined

const isExpressionWrapper = (node: AstNode): node is ExpressionWrapper =>
  node.type === "ChainExpression" ||
  node.type === "ParenthesizedExpression" ||
  node.type === "TSNonNullExpression" ||
  node.type === "TSAsExpression" ||
  node.type === "TSTypeAssertion"

/** Strips chain/parenthesis/TS-assertion wrappers so `(Bun!).spawn` and
 * `Bun?.spawn` resolve to the same underlying expression. */
export const unwrapExpression = (node: unknown): AstNode | undefined => {
  let current = asAstNode(node)
  while (current !== undefined && isExpressionWrapper(current)) {
    current = asAstNode(current.expression)
  }
  return current
}

/** Identifier, private identifier, or string-literal property name. */
export const getPropertyName = (node: unknown): string | undefined => {
  const expression = asAstNode(node)
  if (expression === undefined) return undefined
  if (expression.type === "Identifier" && typeof expression.name === "string") {
    return expression.name
  }
  if (expression.type === "Literal" && typeof expression.value === "string") {
    return expression.value
  }
  return undefined
}

export const isIdentifier = (node: AstNode | undefined, name: string): boolean =>
  node !== undefined && node.type === "Identifier" && node.name === name

export const getImportSource = (node: unknown): string | undefined => {
  const declaration = asAstNode(node)
  if (declaration === undefined || declaration.type !== "ImportDeclaration") return undefined
  return typeof declaration.source.value === "string" ? declaration.source.value : undefined
}
