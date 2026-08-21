import { describe, expect, it } from "vitest"
import {
  CLOSE_DETACH,
  CLOSE_TERMINAL_ENDED,
} from "../../app-server/src/terminals/attachProtocol.ts"
import { classifyClose, DETACH_BYTE } from "../src/attachment.ts"

describe("attachment input", () => {
  it("uses zmx's Ctrl-\\ detach binding", () => {
    expect(DETACH_BYTE).toBe(0x1c)
  })
})

describe("classifyClose", () => {
  it("distinguishes deliberate detach from terminal end and transport loss", () => {
    expect(classifyClose(1000, CLOSE_DETACH, false, 10).type).toBe("detached")
    expect(classifyClose(1000, CLOSE_TERMINAL_ENDED, false, 10).type).toBe("terminalEnded")
    expect(classifyClose(1006, "", false, 10)).toEqual({
      type: "retryable",
      reason: "1006 no reason",
      livedMillis: 10,
    })
  })

  it("lets local detach win a close race", () => {
    expect(classifyClose(1006, "", true, 0).type).toBe("detached")
  })
})
