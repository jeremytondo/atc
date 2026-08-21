import { createTestRenderer } from "@opentui/core/testing"
import { describe, expect, it } from "vitest"
import { StyledSelectRenderable } from "../src/styledSelect.ts"

describe("StyledSelectRenderable", () => {
  it("uses one consistent background across selected text and empty cells", async () => {
    const setup = await createTestRenderer({
      width: 20,
      height: 6,
      exitOnCtrlC: false,
      exitSignals: [],
      useMouse: false,
      autoFocus: false,
    })

    try {
      const list = new StyledSelectRenderable(setup.renderer, {
        width: "100%",
        height: "100%",
        options: [{ name: "Alpha", description: "metadata" }],
        selectedBackgroundColor: "#73737340",
      })
      setup.renderer.root.add(list)
      setup.renderer.start()
      await setup.renderOnce()
      await setup.flush()

      const backgrounds = setup
        .captureSpans()
        .lines.slice(0, 2)
        .flatMap((line) => line.spans.map((span) => span.bg.toInts()))
      expect(new Set(backgrounds.map((color) => color.join(",")))).toHaveLength(1)
    } finally {
      setup.renderer.destroy()
    }
  })
})
