import { describe, expect, it } from "vitest"
import { styledThreadName } from "../src/openTuiApp.ts"

const markerColor = (tone: Parameters<typeof styledThreadName>[1], animationFrame = 0) =>
  styledThreadName("●", tone, "Thread", animationFrame).chunks[0]?.fg?.toInts()

describe("OpenTUI app shell", () => {
  it("gives Thread status markers their semantic colors", () => {
    expect(markerColor("new")).toEqual([45, 212, 191, 255])
    expect(markerColor("idle")).toEqual([100, 116, 139, 255])
    expect(markerColor("attention")).toEqual([251, 113, 133, 255])
    expect(markerColor("unknown")).toEqual([148, 163, 184, 255])
  })

  it("pulses a running marker through yellow intensity", () => {
    expect(markerColor("running", 0)).toEqual([180, 83, 9, 255])
    expect(markerColor("running", 1)).toEqual([194, 103, 8, 255])
    expect(markerColor("running", 5)).toEqual([251, 191, 36, 255])
    expect(markerColor("running", 9)).toEqual([194, 103, 8, 255])
  })
})
