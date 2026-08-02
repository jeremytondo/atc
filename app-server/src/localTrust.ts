import { Effect, Option } from "effect"
import { HttpRouter, HttpServerRequest, HttpServerResponse } from "effect/unstable/http"

// Local trust for the loopback listener (ATC-121): the server only ever binds
// 127.0.0.1, and this middleware rejects the one attack class that still
// reaches a loopback control plane from a browser — DNS rebinding (a hostile
// name resolving to 127.0.0.1 puts its own name in Host) and CSRF (a hostile
// page's request carries its Origin). Requests must present a recognized
// loopback Host, and browser requests (Origin present) must come from a
// loopback origin. Non-browser clients send no Origin and pass untouched.
// Origin-less browser requests (e.g. a cross-site <img>) can only be simple
// GETs — safe while every GET is side-effect-free and responses are
// unreadable cross-origin; revisit if a GET ever gains a side effect.
// The remote-auth pass later relaxes this deliberately for tokened clients.

const LOOPBACK_NAMES = new Set(["localhost", "127.0.0.1", "[::1]"])

/** `Host` header values that identify this loopback server (optional port). */
export const isLoopbackHost = (host: string | undefined): boolean => {
  if (host === undefined) return false
  // Split a trailing :port, respecting the [::1]:port bracket form.
  const name = host.startsWith("[") ? host.replace(/\]:\d+$/, "]") : host.replace(/:\d+$/, "")
  return LOOPBACK_NAMES.has(name.toLowerCase())
}

/** `Origin` header values acceptable on a loopback control plane. */
export const isLoopbackOrigin = (origin: string): boolean => {
  let url: URL
  try {
    url = new URL(origin)
  } catch {
    // Includes the opaque "null" origin (sandboxed iframes, file://): those
    // are exactly the anonymous browser contexts this guard exists to stop.
    return false
  }
  return (url.protocol === "http:" || url.protocol === "https:") && LOOPBACK_NAMES.has(url.hostname)
}

/**
 * Global route middleware: reject spoofed-`Host`/cross-`Origin` requests with
 * an empty 403, and annotate every log line in an accepted request with the
 * request span ids so file logs correlate per request.
 */
export const middleware = HttpRouter.middleware(
  Effect.fnUntraced(function* <E, R>(
    httpEffect: Effect.Effect<
      HttpServerResponse.HttpServerResponse,
      E,
      HttpServerRequest.HttpServerRequest | R
    >,
  ) {
    const request = yield* HttpServerRequest.HttpServerRequest
    const origin = request.headers["origin"]
    if (
      !isLoopbackHost(request.headers["host"]) ||
      (origin !== undefined && !isLoopbackOrigin(origin))
    ) {
      return HttpServerResponse.empty({ status: 403 })
    }
    const span = yield* Effect.option(Effect.currentParentSpan)
    return yield* Option.match(span, {
      onNone: () => httpEffect,
      onSome: (current) =>
        Effect.annotateLogs(httpEffect, { traceId: current.traceId, spanId: current.spanId }),
    })
  }),
  { global: true },
)
