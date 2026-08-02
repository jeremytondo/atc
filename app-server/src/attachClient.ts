import { Effect } from "effect"

// The CLI side of `atc terminal attach` (ATC-130): a raw-terminal WebSocket
// client bridging the local TTY onto the server's attach endpoint — the
// e2e vehicle for the whole terminal transport. Binary frames are terminal
// bytes both ways; text frames are the JSON control protocol (resize from
// us, ping/pong keepalive from the server). Ctrl-] detaches locally
// (the session keeps running server-side).

const DETACH_BYTE = 0x1d // Ctrl-]

export interface AttachResult {
  readonly code: number
  readonly reason: string
}

/**
 * Run one attach session over `url` until the socket closes or the user
 * detaches. Puts stdin into raw mode for the duration when it is a TTY.
 * Callers pre-flight the terminal over the typed API, so a handshake
 * failure here really is a connection problem.
 */
export const runAttach = (url: URL): Effect.Effect<AttachResult, Error> =>
  Effect.callback<AttachResult, Error>((resume) => {
    const stdin = process.stdin
    const wasRaw = stdin.isTTY === true && stdin.isRaw
    const socket = new WebSocket(url)
    socket.binaryType = "arraybuffer"

    let done = false
    const cleanup = () => {
      if (done) return
      done = true
      if (stdin.isTTY === true) stdin.setRawMode(wasRaw)
      stdin.pause()
      stdin.off("data", onStdin)
      process.off("SIGWINCH", onResize)
      if (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING) {
        socket.close(1000, "detach")
      }
    }
    const succeed = (result: AttachResult) => {
      const first = !done
      cleanup()
      if (first) resume(Effect.succeed(result))
    }
    const fail = (error: Error) => {
      const first = !done
      cleanup()
      if (first) resume(Effect.fail(error))
    }

    const onStdin = (chunk: Buffer) => {
      const detachAt = chunk.indexOf(DETACH_BYTE)
      const payload = detachAt === -1 ? chunk : chunk.subarray(0, detachAt)
      if (payload.length > 0 && socket.readyState === WebSocket.OPEN) {
        socket.send(new Uint8Array(payload))
      }
      if (detachAt !== -1) succeed({ code: 1000, reason: "detach" })
    }

    const onResize = () => {
      if (socket.readyState === WebSocket.OPEN && process.stdout.isTTY === true) {
        socket.send(
          JSON.stringify({
            type: "resize",
            cols: process.stdout.columns,
            rows: process.stdout.rows,
          }),
        )
      }
    }

    socket.onopen = () => {
      if (stdin.isTTY === true) stdin.setRawMode(true)
      stdin.resume()
      stdin.on("data", onStdin)
      process.on("SIGWINCH", onResize)
    }
    socket.onmessage = (event) => {
      if (typeof event.data === "string") {
        try {
          const control = JSON.parse(event.data) as { type?: string }
          if (control.type === "ping") socket.send(JSON.stringify({ type: "pong" }))
        } catch {
          // Unknown control frames are ignored.
        }
        return
      }
      process.stdout.write(new Uint8Array(event.data as ArrayBuffer))
    }
    socket.onerror = () => {
      fail(new Error(`cannot connect to ${url.host}; is the App Server running?`))
    }
    socket.onclose = (event) => {
      succeed({ code: event.code, reason: event.reason })
    }

    // Interruption (e.g. SIGINT through runMain): restore the terminal and
    // release the socket; the fiber's own exit reports the interruption.
    return Effect.sync(cleanup)
  })
