import { Option, Schema } from "effect"
import type { SessionSize } from "./terminalAdapter.ts"

// The attach wire protocol (ATC-130), stated once for every side of the
// socket: the server bridge (terminalAttach.ts), the CLI client
// (attachClient.ts), and any future client. The contract endpoint
// description (contract.ts `attachTerminal`) documents it in prose; this module
// is the code authority both sides import.
//
// Binary frames are terminal bytes in both directions. Text frames are the
// JSON control frames below. Close vocabulary: 1000 `terminal_ended` is
// authoritative (a complete inventory proved the terminal ended) and 1000
// `detach` is a deliberate client detach; 1011 reasons are retryable.

const ControlFrame = Schema.Union([
  Schema.Struct({ type: Schema.Literal("resize"), cols: Schema.Number, rows: Schema.Number }),
  /** Server keepalive; a client answers with pong. */
  Schema.Struct({ type: Schema.Literal("ping") }),
  Schema.Struct({ type: Schema.Literal("pong") }),
])
export type ControlFrame = typeof ControlFrame.Type

const decodeFrame = Schema.decodeUnknownOption(ControlFrame)

/** Decode one text frame; unknown or malformed frames are undefined, never fatal. */
export const decodeControlFrame = (text: string): ControlFrame | undefined => {
  try {
    return Option.getOrUndefined(decodeFrame(JSON.parse(text)))
  } catch {
    return undefined
  }
}

export const encodeControlFrame = (frame: ControlFrame): string => JSON.stringify(frame)

/**
 * The one dimension rule for resize frames and `cols`/`rows` query params:
 * missing or malformed values take the fallback, everything else is floored
 * and clamped into [1, 1000].
 */
export const clampDimension = (raw: string | number | undefined, fallback: number): number => {
  const parsed = typeof raw === "number" ? raw : Number.parseInt(raw ?? "", 10)
  return Number.isNaN(parsed) ? fallback : Math.max(1, Math.min(1_000, Math.floor(parsed)))
}

/** Close (1000) reason proving the terminal ended — not retryable. */
export const CLOSE_TERMINAL_ENDED = "terminal_ended"
/** Close (1000) reason for a deliberate client detach. */
export const CLOSE_DETACH = "detach"
/** Retryable close (1011) reason: the attach could not be completed. */
export const CLOSE_ATTACH_FAILED = "attach_failed"
/** Retryable close (1011) reason: the multiplexer could not be consulted. */
export const CLOSE_ZMX_UNAVAILABLE = "zmx_unavailable"
/** Retryable close (1011) reason: the client stopped answering pings. */
export const CLOSE_PING_TIMEOUT = "ping_timeout"

/** The contract's `attachTerminal` route as a WebSocket URL. */
export const attachUrl = (baseUrl: URL, terminalId: string, size?: SessionSize): URL => {
  const url = new URL(`/api/v1/terminals/${encodeURIComponent(terminalId)}/attach`, baseUrl)
  url.protocol = baseUrl.protocol === "https:" ? "wss:" : "ws:"
  if (size !== undefined) {
    url.searchParams.set("cols", String(size.cols))
    url.searchParams.set("rows", String(size.rows))
  }
  return url
}
