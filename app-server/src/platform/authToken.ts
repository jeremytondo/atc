import { Context, Effect, FileSystem, Layer } from "effect"
import { timingSafeEqual } from "node:crypto"
import * as path from "node:path"
import { AppConfig } from "./config.ts"

// The remote-access credential (ATC-148): one static bearer token,
// auto-generated on first server start and persisted as a 0600 file in the
// data dir (SSH-host-key style). Loopback requests never consult it — it only
// gates non-loopback requests in the trust middleware (api/localTrust.ts).
//
// Rotation rule: `verify` re-reads the file on every check instead of caching
// at startup, so `atc token rotate` takes effect immediately — the old token
// stops working without a server restart, and there is no cache-invalidation
// machinery to get wrong. The read cost is paid only by non-loopback
// requests presenting a token. A missing or unreadable file fails closed.

/** Prefixed 256-bit random token; the prefix makes leaks greppable. */
const generate = (): string => {
  const bytes = new Uint8Array(32)
  crypto.getRandomValues(bytes)
  return `atc_${Buffer.from(bytes).toString("base64url")}`
}

// Byte-wise timing-safe comparison. Length is not secret (the format is
// public), so a length mismatch may return early.
const tokenEquals = (presented: string, actual: string): boolean => {
  const a = Buffer.from(presented)
  const b = Buffer.from(actual)
  return a.length === b.length && timingSafeEqual(a, b)
}

/** The stored token, or undefined when the file does not exist yet. */
const readToken = (fs: FileSystem.FileSystem, file: string) =>
  fs.readFileString(file).pipe(
    Effect.map((text) => {
      const token = text.trim()
      return token === "" ? undefined : token
    }),
    Effect.catchTag("PlatformError", (error) =>
      error.reason._tag === "NotFound" ? Effect.succeed(undefined) : Effect.fail(error),
    ),
  )

// Atomic replace: write a fresh 0600 temp file, then rename it over the
// target. The mode is honored unconditionally (it applies at creation of the
// temp, which rename preserves — plain `writeFileString`'s mode option is a
// no-op over an existing file), and a concurrent `verify` never observes a
// truncated or half-written token. Used by rotation.
const replaceToken = (fs: FileSystem.FileSystem, file: string, token: string) =>
  Effect.gen(function* () {
    yield* fs.makeDirectory(path.dirname(file), { recursive: true })
    const temp = `${file}.${crypto.randomUUID()}.tmp`
    yield* fs.writeFileString(temp, `${token}\n`, { flag: "wx", mode: 0o600 })
    yield* fs.rename(temp, file)
    return token
  })

/** Read the persisted token, generating and persisting one if absent. */
export const ensure = Effect.gen(function* () {
  const config = yield* AppConfig
  const fs = yield* FileSystem.FileSystem
  const existing = yield* readToken(fs, config.tokenFile)
  if (existing !== undefined) return existing
  yield* fs.makeDirectory(path.dirname(config.tokenFile), { recursive: true })
  const token = generate()
  // Exclusive create ("wx"): if a concurrent ensure (server boot racing
  // `atc token` on a fresh install) already created the file, adopt the
  // winner's token instead of overwriting it — otherwise each caller could
  // mint and hand out a different token.
  return yield* fs
    .writeFileString(config.tokenFile, `${token}\n`, { flag: "wx", mode: 0o600 })
    .pipe(
      Effect.as(token),
      Effect.catchTag("PlatformError", (error) =>
        error.reason._tag === "AlreadyExists"
          ? readToken(fs, config.tokenFile).pipe(Effect.map((winner) => winner ?? token))
          : Effect.fail(error),
      ),
    )
})

/** Unconditionally reissue the token; the old one stops working at once. */
export const rotate = Effect.gen(function* () {
  const config = yield* AppConfig
  const fs = yield* FileSystem.FileSystem
  return yield* replaceToken(fs, config.tokenFile, generate())
})

/**
 * Bearer-credential check for the trust middleware: does this
 * `Authorization` header value present the current token?
 */
export class AuthToken extends Context.Service<
  AuthToken,
  {
    /** True only for a well-formed `Bearer` header matching the stored token. */
    readonly verify: (authorization: string | undefined) => Effect.Effect<boolean>
  }
>()("app-server/AuthToken") {}

/**
 * Production layer: ensures the token exists at startup (so `atc token` on a
 * fresh install prints the same credential the server enforces) and verifies
 * by re-reading the file, failing closed on any read error.
 */
export const layer = Layer.effect(AuthToken)(
  Effect.gen(function* () {
    const config = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    // Preparing the token must not sink a loopback-only boot: a broken token
    // file (bad permissions, wrong type) leaves remote access unavailable
    // (verify fails closed below), never blocks the server from starting.
    yield* ensure.pipe(
      Effect.catchTag("PlatformError", (error) =>
        Effect.logWarning(`remote-access token unavailable: ${error.message}`),
      ),
    )
    return {
      verify: (authorization) => {
        const presented = authorization?.match(/^Bearer\s+(.+)$/i)?.[1]
        if (presented === undefined) return Effect.succeed(false)
        return readToken(fs, config.tokenFile).pipe(
          Effect.map((token) => token !== undefined && tokenEquals(presented, token)),
          Effect.catch(() => Effect.succeed(false)),
        )
      },
    }
  }),
)
