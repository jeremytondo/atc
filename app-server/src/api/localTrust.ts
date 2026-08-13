import { Effect, Option } from "effect"
import { HttpRouter, HttpServerRequest, HttpServerResponse } from "effect/unstable/http"
import * as AuthToken from "../platform/authToken.ts"
import * as Tailscale from "../platform/tailscale.ts"

// The trust rule, in one sentence: a request passes if it meets the loopback
// Host/Origin rules below, matches the currently verified Tailscale Serve
// Host/Origin, OR presents the bearer token; everything else gets the same
// empty 403. One global middleware covers every HTTP surface uniformly.
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
// Tailscale trust (ATC-184): the server stays loopback-bound while its scoped
// foreground `tailscale serve` child is the only tailnet ingress. A proxied
// request is token-free only while that child is verified running and its
// exact discovered HTTPS host (including ATC's non-443 port) matches Host and
// Origin. Tailnet reachability and ACLs are the authorization boundary. No
// forwarded or identity header is trusted, and losing verification removes
// the allowlist immediately.
//
// Token trust (ATC-148) remains the fallback for every other request when a
// non-loopback bind is configured. Both token-free paths are gated on the TCP
// peer being loopback: Host is client-controlled, so it is never sufficient
// evidence by itself. Loopback stays secret-free for ordinary local clients.

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

const isTailscaleRequest = (
  status: Tailscale.TailscaleStatus,
  host: string | undefined,
  origin: string | undefined,
): boolean => {
  if (status.state !== "running" || !URL.canParse(status.url)) return false
  const url = new URL(status.url)
  return host === url.host && (origin === undefined || origin === url.origin)
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
    const tailscale = yield* Effect.serviceOption(Tailscale.Tailscale)
    const request = yield* HttpServerRequest.HttpServerRequest
    const origin = request.headers["origin"]
    const loopbackPeer = Option.match(request.remoteAddress, {
      onNone: () => false,
      onSome: isLoopbackAddress,
    })
    const local =
      loopbackPeer &&
      isLoopbackHost(request.headers["host"]) &&
      (origin === undefined || isLoopbackOrigin(origin))
    const tailscaleStatus = Option.isSome(tailscale)
      ? yield* tailscale.value.status
      : ({ state: "disabled" } as const)
    const tailnet =
      loopbackPeer && isTailscaleRequest(tailscaleStatus, request.headers["host"], origin)
    if (!local && !tailnet && !(yield* authToken.verify(request.headers["authorization"]))) {
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
