# Idea: remote access & web-console authentication

> **Status: idea / not yet implemented (2026-07-01).** Captures a design
> discussion about how the web console should be secured and reached from another
> machine. No code has changed. When a direction is committed, promote the
> relevant parts to an ADR under `docs/adr/` and a spec.

## Context

The web console is becoming a built-in admin panel for the Go service. It is
intended for **one developer**, not a multi-user platform, and is expected to
always run **alongside the Go app on the same machine** — never standalone.

Two deployment realities shape this:

- The service runs on an **always-on workstation**.
- The developer wants to reach the console from a **laptop**, and both machines
  are already on the same **Tailscale** tailnet (`*.tail1f9a09.ts.net`).
- The current dev loop already uses **SSH `LocalForward`** (5173 + 7331) so that
  `mise run dev` prints `http://127.0.0.1:5173` and clicking it "just works" on
  the laptop. Preserving that low-friction feel is a goal.

## Current security posture (as of this writing)

The server runs two listeners sharing one router:

- A **Unix domain socket** in a `0700` owner-only control dir
  (`internal/paths/paths.go`). The CLI uses this; it is authenticated purely by
  OS file permissions (Docker-style). This is solid and should stay.
- A **loopback TCP socket**, default `127.0.0.1:7331`
  (`internal/server/server.go`).

Authentication is a single middleware, `withAuth`, wrapping only `/api/`
(`internal/server/auth.go`). Its behavior:

1. **Token empty → the guard is a no-op; every request passes.** This is the
   default, because the token is only ever set via `[auth] token` in the TOML or
   `ATC_API_TOKEN`, and nothing sets it. **So today the REST API is
   effectively open to anything that can reach `127.0.0.1:7331`.**
2. Request arrived on the Unix socket → always trusted.
3. Request on TCP with a token configured → must present
   `Authorization: Bearer <token>`.

The "API key" placeholder is a password field in the web sidebar
(`web/src/lib/api.ts`, `web/src/lib/shell.svelte`) that saves to `localStorage`
and adds a Bearer header. With the server-side token empty, it does nothing.

Note: the terminal-attach WebSocket already checks `Origin`
(`internal/api/attach.go`), but the ordinary JSON REST endpoints do **not** — the
only gate is the (disabled) token.

## Key insight: transport vs. app-auth

"Reachable from another machine" is primarily a **transport** question, not an
app-auth question. Two very different architectures:

- **(a) Keep Atelier Code local; secure the transport.** Atelier Code stays bound to
  loopback; the laptop reaches it via an SSH port-forward or a private mesh
  (Tailscale/WireGuard). The tunnel provides encryption *and* authentication.
  Atelier Code barely changes.
- **(b) Make Atelier Code a directly network-exposed service.** Bind to a routable
  address and build real app-level auth + TLS into the Go app, because anything
  on the network can now reach the port.

For a solo dev on one workstation + one laptop, **(a) is far simpler and safer**,
and matches how tools like Jupyter/TensorBoard recommend remote access. Also
worth stating plainly: the terminal-attach endpoint is effectively a **remote
shell**, so exposing it to any network (even a LAN) is exposing shell access —
another reason to prefer a tunnel over a raw open port.

## Transport options (with the "click the link" tradeoff)

The click-magic today comes from **the app advertising a `localhost` URL + that
exact port being tunneled**. Any option that keeps those aligned keeps the feel.

1. **SSH `LocalForward` — current setup; keep it for dev.** `mise run dev` prints
   `localhost:5173`; the forward carries it; Vite proxies `/api` (including the
   attach WS) to `127.0.0.1:7331`. All same-origin `localhost:5173`. Ideal for the
   dev loop; zero code change.
2. **Direct MagicDNS bind.** Bind Atelier Code to the network and browse
   `http://workstation.tail1f9a09.ts.net:7331`. No SSH forward; works whenever the
   box is up. Wire is WireGuard-encrypted within the tailnet. Tradeoff: a stable
   bookmark rather than the `localhost` link, and a non-loopback bind (already
   supported via `--http-addr` / `ATC_HTTP_ADDR`, which warns on non-loopback).
3. **`tailscale serve` — recommended for the always-on console.** Atelier Code stays
   bound to `127.0.0.1:7331`; `tailscale serve` proxies the tailnet name to that
   loopback port, giving `https://workstation.tail1f9a09.ts.net` with real
   (Let's Encrypt) HTTPS, **tailnet-private** (not public internet). Atelier Code stays
   loopback-simple; bookmark one HTTPS URL. (Verify exact CLI syntax with
   `tailscale serve --help` / `tailscale serve status` — it varies by version.)
4. **`tailscale funnel` — avoid.** Same as serve but exposes to the *public
   internet*; wrong tool for a private single-dev console.

Dev-time and the always-on console are different jobs and can coexist: keep SSH
`LocalForward` for `mise run dev`, and use `tailscale serve` for the running
daemon.

## Recommended direction

- **Dev:** keep the SSH `LocalForward` setup unchanged.
- **Always-on console:** put it behind `tailscale serve` (HTTPS, stable URL,
  tailnet-only, Atelier Code stays loopback + Unix-socket simple).

## Future implementation notes

When this is picked up as real work:

- **Keep loopback bind + Unix socket.** The CLI stays authed by the `0700`
  socket; both SSH-forward and `tailscale serve` reach `127.0.0.1:7331`. Only the
  direct-MagicDNS option needs a non-loopback bind.
- **Add a same-origin CSRF/rebinding check on `/api/`, not a hardcoded loopback
  check.** Require the request `Origin`'s host to equal the request `Host`. This
  works identically for `localhost:5173`, `localhost:7331`, and
  `workstation.tail1f9a09.ts.net` with zero config, and closes the
  malicious-browser-page vector. A hardcoded "must be 127.0.0.1" would break
  `tailscale serve` (whose `Host` is the tailnet name).
  - The attach WS is already same-origin-safe; its hardcoded
    `attachOriginPatterns` (`internal/api/attach.go`) only matters for the
    cross-origin dev split at `:5173`.
- **Leave the token deferred/optional.** With serve or the tunnel, the security
  boundary is the tailnet (WireGuard + Tailscale ACLs) plus the same-origin
  check — sufficient for a personal tailnet. Only add an auth credential if other
  people's devices ever join the tailnet, and prefer an **auto-generated token
  injected by the server into its own page** (Jupyter-style, no manual typing)
  over the current manual sidebar field.
- **Clean up the dead token UI.** Either remove the sidebar password field +
  `localStorage` token plumbing (`web/src/lib/api.ts`, `web/src/lib/shell.svelte`)
  or repurpose it for the auto-injected-token path above, so the app is honest
  about what actually gates access.

## Related code

- `internal/server/auth.go` — `withAuth`, the no-op-on-empty-token seam.
- `internal/server/server.go` — dual listeners, `DefaultHTTPAddr`, non-loopback
  bind warning.
- `internal/server/listener.go` — TCP/Unix listener boundary.
- `internal/config/config.go` — `AuthConfig.Token`, `ATC_API_TOKEN`.
- `internal/api/attach.go` — attach WebSocket `Origin` handling.
- `internal/paths/paths.go` — `0700` control dir for the Unix socket.
- `web/src/lib/api.ts`, `web/src/lib/shell.svelte` — sidebar token field.
