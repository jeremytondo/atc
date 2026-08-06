import { assert, describe, it } from "@effect/vitest"
import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import {
  attachUrl,
  clampDimension,
  CLOSE_ATTACH_FAILED,
  CLOSE_DETACH,
  CLOSE_PING_TIMEOUT,
  CLOSE_TERMINAL_ENDED,
  CLOSE_ZMX_UNAVAILABLE,
  decodeControlFrame,
  encodeControlFrame,
} from "../../src/terminals/attachProtocol.ts"
import type { ControlFrame } from "../../src/terminals/attachProtocol.ts"

// The shared attach-protocol vectors (packages/attach-protocol/fixtures/),
// run against the TypeScript implementation. ATCKit's attach client must
// consume the same files, so protocol drift between clients fails a test
// instead of surfacing in a terminal window.

const fixture = <T>(name: string): T =>
  JSON.parse(
    readFileSync(
      fileURLToPath(new URL(`../../../packages/attach-protocol/fixtures/${name}`, import.meta.url)),
      "utf8",
    ),
  ) as T

describe("attach protocol fixtures", () => {
  it("round-trips every valid control frame and ignores every malformed one", () => {
    const frames = fixture<{
      valid: Array<{ name: string; wire: string; frame: ControlFrame }>
      ignored: Array<{ name: string; wire: string }>
    }>("control-frames.json")
    for (const { name, wire, frame } of frames.valid) {
      assert.deepStrictEqual(decodeControlFrame(wire), frame, name)
      assert.deepStrictEqual(decodeControlFrame(encodeControlFrame(frame)), frame, name)
    }
    for (const { name, wire } of frames.ignored) {
      assert.isUndefined(decodeControlFrame(wire), name)
    }
  })

  it("the close vocabulary matches the exported constants exactly", () => {
    const { closes } = fixture<{
      closes: Array<{ code: number; reason: string | null; retryable: boolean }>
    }>("close-codes.json")
    const reasons = closes.flatMap((close) => close.reason ?? [])
    assert.sameMembers(reasons, [
      CLOSE_TERMINAL_ENDED,
      CLOSE_DETACH,
      CLOSE_ATTACH_FAILED,
      CLOSE_ZMX_UNAVAILABLE,
      CLOSE_PING_TIMEOUT,
    ])
    // The semantic pins clients build retry logic on.
    const byReason = new Map(closes.map((close) => [close.reason, close]))
    assert.deepInclude(byReason.get(CLOSE_TERMINAL_ENDED), { code: 1000, retryable: false })
    assert.deepInclude(byReason.get(CLOSE_DETACH), { code: 1000 })
    for (const reason of [CLOSE_ATTACH_FAILED, CLOSE_ZMX_UNAVAILABLE, CLOSE_PING_TIMEOUT]) {
      assert.deepInclude(byReason.get(reason), { code: 1011, retryable: true })
    }
    assert.deepInclude(byReason.get(null), { code: 1006, retryable: true })
  })

  it("applies the dimension floor/clamp/fallback rule to every case", () => {
    const clamp = fixture<{
      fallback: number
      cases: Array<{ name: string; raw: string | number | null; expect: number }>
    }>("dimension-clamp.json")
    for (const { name, raw, expect } of clamp.cases) {
      assert.strictEqual(clampDimension(raw ?? undefined, clamp.fallback), expect, name)
    }
  })

  it("builds every attach URL case", () => {
    const urls = fixture<{
      cases: Array<{
        name: string
        base: string
        terminalId: string
        cols?: number
        rows?: number
        expect: string
      }>
    }>("attach-urls.json")
    for (const { name, base, terminalId, cols, rows, expect } of urls.cases) {
      const size = cols !== undefined && rows !== undefined ? { cols, rows } : undefined
      assert.strictEqual(attachUrl(new URL(base), terminalId, size).toString(), expect, name)
    }
  })
})
