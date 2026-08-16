// A minimal RFC 6455 WebSocket client over a unix domain socket (ATC-196).
// Bun's global `WebSocket` only dials TCP, while the codex app-server's
// supported local transport is a WebSocket served on its unix control
// socket, so this module speaks the protocol itself: the Upgrade handshake,
// masked text frames out, fragment reassembly in, ping/pong, and the close
// handshake. Nothing more — no extensions, no subprotocols, and binary
// payloads are handed over decoded as text (a JSON-RPC peer never sends
// any). The surface mirrors the browser WebSocket so a caller can swap one
// for the other: construct, assign handlers synchronously, then wait for
// `onopen`. Nothing fires before the caller's current task ends — the
// connect is deferred a microtask to guarantee it.

const GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
const OPCODE_CONTINUATION = 0x0
const OPCODE_TEXT = 0x1
const OPCODE_CLOSE = 0x8
const OPCODE_PING = 0x9
const OPCODE_PONG = 0xa
/** Bound on one incoming message; a header past it is a protocol failure. */
const MAX_MESSAGE_BYTES = 256 * 1024 * 1024

const encoder = new TextEncoder()
const decoder = new TextDecoder()

export interface Connection {
  /** The handshake completed; `send` is live from here. */
  onopen: (() => void) | null
  /** One complete text message (fragments already reassembled). */
  onmessage: ((text: string) => void) | null
  /**
   * The connection is gone — refused, handshake rejected, peer closed, socket
   * error, or a local `close()`. Fires exactly once, before or after
   * `onopen`; `reason` is a short human-readable cause.
   */
  onclose: ((reason: string) => void) | null
  /** Queue one text message; silently dropped once the connection is gone. */
  readonly send: (text: string) => void
  /**
   * Close: the closing handshake when open (onclose follows once the peer
   * echoes, bounded), an immediate release otherwise. Idempotent.
   */
  readonly close: () => void
}

/** Encode one client frame: FIN set, masked as RFC 6455 requires of clients. */
const encodeFrame = (opcode: number, payload: Uint8Array): Uint8Array => {
  const length = payload.length
  const header =
    length < 126
      ? [0x80 | opcode, 0x80 | length]
      : length < 65_536
        ? [0x80 | opcode, 0x80 | 126, length >>> 8, length & 0xff]
        : [
            0x80 | opcode,
            0x80 | 127,
            0,
            0,
            0,
            0,
            (length >>> 24) & 0xff,
            (length >>> 16) & 0xff,
            (length >>> 8) & 0xff,
            length & 0xff,
          ]
  const mask = crypto.getRandomValues(new Uint8Array(4))
  const frame = new Uint8Array(header.length + 4 + length)
  frame.set(header, 0)
  frame.set(mask, header.length)
  const offset = header.length + 4
  for (let index = 0; index < length; index++) {
    frame[offset + index] = payload[index]! ^ mask[index & 3]!
  }
  return frame
}

const concat = (left: Uint8Array, right: Uint8Array): Uint8Array => {
  if (left.length === 0) return right
  const joined = new Uint8Array(left.length + right.length)
  joined.set(left, 0)
  joined.set(right, left.length)
  return joined
}

/**
 * Open a WebSocket connection to the server listening on `socketPath`. The
 * connection is returned immediately; assign handlers before yielding to
 * the event loop.
 */
export const open = (socketPath: string): Connection => {
  const key = btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(16))))
  const expectedAccept = new Bun.CryptoHasher("sha1").update(key + GUID).digest("base64")

  let socket: Bun.Socket<undefined> | null = null
  let handshaken = false
  let closing = false
  let finished = false
  let closeTimer: ReturnType<typeof setTimeout> | null = null
  let buffered: Uint8Array = new Uint8Array(0)
  // Reassembly of a fragmented message: the first fragment's opcode and
  // the payload so far.
  let fragmentOpcode: number | null = null
  let fragments: Uint8Array = new Uint8Array(0)
  // Bytes the kernel would not take yet, flushed on `drain`.
  const backlog: Array<Uint8Array> = []

  const connection: Connection = {
    onopen: null,
    onmessage: null,
    onclose: null,
    send: (text) => {
      if (handshaken && !closing) write(encodeFrame(OPCODE_TEXT, encoder.encode(text)))
    },
    close: () => {
      if (finished || closing) return
      if (socket === null || !handshaken) {
        finish("closed")
        return
      }
      // The closing handshake: send close, let the peer echo it (or the
      // socket drop) before ending — a FIN right behind our close frame
      // reads as an abnormal closure to the peer. Bounded: a peer that
      // never echoes is cut off.
      closing = true
      write(encodeFrame(OPCODE_CLOSE, new Uint8Array(0)))
      closeTimer = setTimeout(() => finish("closed"), 1_000)
    },
  }

  const finish = (reason: string): void => {
    if (finished) return
    finished = true
    if (closeTimer !== null) clearTimeout(closeTimer)
    // FIN goes out behind whatever is still queued (a close echo, say) —
    // bounded, since a stalled peer may never drain it. A socket that never
    // connected has nothing to end and is terminated on arrival below.
    if (backlog.length === 0) socket?.end()
    else setTimeout(() => socket?.terminate(), 1_000)
    connection.onclose?.(reason)
  }

  /** Raw bytes to the peer, queued past what the kernel takes at once. */
  const write = (bytes: Uint8Array): void => {
    if (finished || socket === null) return
    if (backlog.length > 0) {
      backlog.push(bytes)
      return
    }
    const written = socket.write(bytes)
    if (written < bytes.length) backlog.push(bytes.subarray(Math.max(written, 0)))
  }

  const drain = (): void => {
    while (backlog.length > 0) {
      if (socket === null) return
      const next = backlog[0]!
      const written = socket.write(next)
      if (written < next.length) {
        backlog[0] = next.subarray(Math.max(written, 0))
        return
      }
      backlog.shift()
    }
    if (finished) socket?.end()
  }

  const deliver = (opcode: number, payload: Uint8Array): void => {
    if (opcode === OPCODE_TEXT || opcode === OPCODE_CONTINUATION) {
      connection.onmessage?.(decoder.decode(payload))
      return
    }
    if (opcode === OPCODE_PING) {
      write(encodeFrame(OPCODE_PONG, payload))
      return
    }
    if (opcode === OPCODE_CLOSE) {
      // Their echo of our close completes our handshake; their own close
      // is echoed so they can complete theirs. Either way we are done.
      if (closing) {
        finish("closed")
        return
      }
      write(encodeFrame(OPCODE_CLOSE, payload.subarray(0, 2)))
      const code = payload.length >= 2 ? (payload[0]! << 8) | payload[1]! : null
      finish(code === null ? "closed by peer" : `closed by peer (${code})`)
    }
    // Pong and reserved opcodes carry nothing for us.
  }

  /** Consume every complete frame in `buffered`; partial frames wait. */
  const parseFrames = (): void => {
    while (buffered.length >= 2) {
      // A close frame mid-buffer ends parsing; whatever follows is noise.
      if (finished) return
      const fin = (buffered[0]! & 0x80) !== 0
      const opcode = buffered[0]! & 0x0f
      const masked = (buffered[1]! & 0x80) !== 0
      let length = buffered[1]! & 0x7f
      let offset = 2
      if (length === 126) {
        if (buffered.length < 4) return
        length = (buffered[2]! << 8) | buffered[3]!
        offset = 4
      } else if (length === 127) {
        if (buffered.length < 10) return
        const high =
          ((buffered[2]! << 24) >>> 0) + ((buffered[3]! << 16) | (buffered[4]! << 8) | buffered[5]!)
        const low =
          ((buffered[6]! << 24) >>> 0) + ((buffered[7]! << 16) | (buffered[8]! << 8) | buffered[9]!)
        length = high * 2 ** 32 + low
        offset = 10
      }
      // A length no JSON-RPC peer would send (or a corrupt header) is a
      // protocol failure, never a buffer to wait for.
      if (fragments.length + length > MAX_MESSAGE_BYTES) {
        finish(`protocol error: frame of ${length} bytes exceeds the limit`)
        return
      }
      const maskOffset = offset
      if (masked) offset += 4
      if (buffered.length < offset + length) return
      const payload = buffered.slice(offset, offset + length)
      if (masked) {
        for (let index = 0; index < length; index++) {
          payload[index] = payload[index]! ^ buffered[maskOffset + (index & 3)]!
        }
      }
      buffered = buffered.subarray(offset + length)
      // Control frames may interleave fragments; data frames reassemble.
      if (opcode >= 0x8) {
        deliver(opcode, payload)
        continue
      }
      if (opcode !== OPCODE_CONTINUATION) fragmentOpcode = opcode
      fragments = concat(fragments, payload)
      if (!fin) continue
      const message = fragments
      const messageOpcode = fragmentOpcode ?? opcode
      fragments = new Uint8Array(0)
      fragmentOpcode = null
      deliver(messageOpcode, message)
    }
  }

  const completeHandshake = (): void => {
    const text = decoder.decode(buffered)
    const end = text.indexOf("\r\n\r\n")
    if (end < 0) return
    const head = text.slice(0, end)
    // The head is ASCII: its character count is its byte count.
    buffered = buffered.subarray(end + 4)
    const [statusLine = "", ...headerLines] = head.split("\r\n")
    const accept = headerLines
      .map((line) => line.split(/:\s*/, 2))
      .find(([name]) => name?.toLowerCase() === "sec-websocket-accept")?.[1]
      ?.trim()
    if (!statusLine.startsWith("HTTP/1.1 101") || accept !== expectedAccept) {
      finish(`handshake rejected: ${statusLine || "no HTTP status line"}`)
      return
    }
    handshaken = true
    connection.onopen?.()
    parseFrames()
  }

  // Deferred one microtask: an immediate failure (no such socket) would
  // otherwise report `connectError` synchronously inside connect(), before
  // the caller has assigned its handlers.
  queueMicrotask(() => {
    if (finished) return
    Bun.connect<undefined>({
      unix: socketPath,
      socket: {
        open: (opened) => {
          if (finished) {
            opened.terminate()
            return
          }
          socket = opened
          write(
            encoder.encode(
              `GET / HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n` +
                `Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: ${key}\r\n\r\n`,
            ),
          )
        },
        data: (_, chunk) => {
          if (finished) return
          buffered = concat(buffered, new Uint8Array(chunk))
          if (!handshaken) completeHandshake()
          else parseFrames()
        },
        drain: () => drain(),
        close: () => finish("connection closed"),
        end: () => finish("connection closed by peer"),
        error: (_, error) => finish(`socket error: ${error.message}`),
        connectError: (_, error) => finish(`connect failed: ${error.message}`),
      },
    }).catch((error: unknown) => {
      finish(`connect failed: ${error instanceof Error ? error.message : String(error)}`)
    })
  })

  return connection
}
