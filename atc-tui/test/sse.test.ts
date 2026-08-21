import { describe, expect, it } from "vitest"
import { SseParser, backoffMillis } from "../src/sse.ts"

describe("SseParser", () => {
  it("preserves framing across chunks and surfaces comments immediately", () => {
    const parser = new SseParser()
    const first = parser.consume(new TextEncoder().encode(": con"))
    const second = parser.consume(
      new TextEncoder().encode('nected\r\n\r\ndata: {"resource":"thread",'),
    )
    const third = parser.consume(
      new TextEncoder().encode('"id":"t1","change":"updated"}\n\n: heartbeat\n\n'),
    )

    expect(first).toEqual([])
    expect(second).toEqual([{ type: "comment", value: "connected" }])
    expect(third).toEqual([
      {
        type: "data",
        value: '{"resource":"thread","id":"t1","change":"updated"}',
      },
      { type: "comment", value: "heartbeat" },
    ])
  })

  it("joins multiple data lines", () => {
    const parser = new SseParser()
    expect(parser.consume(new TextEncoder().encode("data: one\ndata: two\n\n"))).toEqual([
      { type: "data", value: "one\ntwo" },
    ])
  })
})

describe("backoffMillis", () => {
  it("doubles and caps at eight seconds", () => {
    expect([0, 1, 2, 3, 4, 10].map(backoffMillis)).toEqual([500, 1_000, 2_000, 4_000, 8_000, 8_000])
  })
})
