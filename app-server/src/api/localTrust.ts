import { Effect, Option } from "effect"
import { HttpRouter, HttpServerRequest, HttpServerResponse } from "effect/unstable/http"
import * as AuthToken from "../platform/authToken.ts"

// The trust rule, in one sentence: a request passes if it meets the loopback
// Host/Origin rules below OR presents the bearer token (ATC-148); everything
// else gets the same empty 403. One global middleware, so the API, SSE, the
// attach WebSocket upgrade, and the hook webhook are covered uniformly.
//
// Loopback trust (ATC-121): the default listener binds 127.0.0.1, and the
// Host/Origin rules reject the one attack class that still reaches a
// loopback control plane from a browser — DNS rebinding (a hostile name
// resolving to 127.0.0.1 puts its own name in Host) and CSRF (a hostile
// page's request carries its Origin). Requests must present a recognized
// loopback Host, and browser requests (Origin present) must come from a
// loopback origin. Non-browser clients send no Origin and pass untouched.
// Origin-less browser requests (e.g. a cross-site <img>) can only be simple
// GETs — safe while every GET is side-effect-free and responses are
// unreadable cross-origin; revisit if a GET ever gains a side effect.
//
// Token trust (ATC-148): with the `bind` setting opened beyond loopback
// (intended posture: tailnet-only reachability), non-loopback requests pass
// only with `Authorization: Bearer <token>` matching the persisted token
// (platform/authToken.ts, timing-safe). Loopback stays token-free: local
// clients ("no secrets, just the URL") are untouched. Browsers cannot attach
// bearer headers to SSE/WebSockets, so remote browser access 403s by design.
//
// The token-free path is gated on the TCP PEER address, never on the `Host`
// header alone: `Host` is client-controlled, so trusting a "loopback" Host
// from a non-loopback peer would let any remote client send `Host: localhost`
// and skip the token entirely. Peer-gating also keeps `tailscale serve`
// working token-free — it proxies to us over loopback — while a direct
// tailnet/LAN connection (a non-loopback peer) must present the token.

const LOOPBACK_NAMES = new Set(["localhost", "127.0.0.1", "[::1]"])

// Peer addresses Bun reports for a loopback connection: IPv4, IPv6, and the
// IPv4-mapped-IPv6 form. Any 127.0.0.0/8 address is loopback too.
export const isLoopbackAddress = (address: string): boolean =>
  address === "::1" ||
  address === "::ffff:127.0.0.1" ||
  address.startsWith("127.") ||
  address.startsWith("::ffff:127.")

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
 * Global route middleware: reject requests that neither satisfy the loopback
 * `Host`/`Origin` rules nor present the bearer token with an empty 403, and
 * annotate every log line in an accepted request with the request span ids
 * so file logs correlate per request.
 */
export const middleware = HttpRouter.middleware(
  Effect.fnUntraced(function* <E, R>(
    httpEffect: Effect.Effect<
      HttpServerResponse.HttpServerResponse,
      E,
      HttpServerRequest.HttpServerRequest | R
    >,
  ) {
    const authToken = yield* AuthToken.AuthToken
    const request = yield* HttpServerRequest.HttpServerRequest
    const origin = request.headers["origin"]
    // A loopback PEER (unknown peer fails closed) whose Host/Origin also pass
    // is the token-free local path; every other request needs the token.
    const loopback =
      Option.match(request.remoteAddress, { onNone: () => false, onSome: isLoopbackAddress }) &&
      isLoopbackHost(request.headers["host"]) &&
      (origin === undefined || isLoopbackOrigin(origin))
    if (!loopback && !(yield* authToken.verify(request.headers["authorization"]))) {
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
