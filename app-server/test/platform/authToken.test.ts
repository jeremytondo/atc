import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import * as AuthToken from "../../src/platform/authToken.ts"
import { trackTempDir } from "../blackbox.ts"
import { testAppConfig } from "../testLayers.ts"

// The remote-access credential (ATC-148): generation is one-shot and 0600,
// and `verify` follows the file — rotation applies immediately, a missing
// file fails closed. Real filesystem because permissions and re-reads are
// exactly what is under test.

const withTokenDir = () => {
  const dir = trackTempDir(fs.mkdtempSync(path.join(os.tmpdir(), "atc-token-")))
  const tokenFile = path.join(dir, "data", "auth-token")
  const config = testAppConfig({ tokenFile })
  return { tokenFile, config }
}

describe("auth token", () => {
  it.effect("ensure generates once, persists 0600, and stays stable", () => {
    const { tokenFile, config } = withTokenDir()
    return Effect.gen(function* () {
      const first = yield* AuthToken.ensure
      assert.match(first, /^atc_[A-Za-z0-9_-]{43}$/)
      assert.strictEqual(fs.statSync(tokenFile).mode & 0o777, 0o600)
      assert.strictEqual(fs.readFileSync(tokenFile, "utf8"), `${first}\n`)
      assert.strictEqual(yield* AuthToken.ensure, first)
    }).pipe(Effect.provide([config, BunServices.layer]))
  })

  it.effect("verify follows the file: rotation applies immediately, missing fails closed", () => {
    const { tokenFile, config } = withTokenDir()
    return Effect.gen(function* () {
      const auth = yield* AuthToken.AuthToken
      const token = yield* AuthToken.ensure

      assert.isTrue(yield* auth.verify(`Bearer ${token}`))
      // The scheme is case-insensitive; the token is not.
      assert.isTrue(yield* auth.verify(`bearer ${token}`))
      assert.isFalse(yield* auth.verify(token))
      assert.isFalse(yield* auth.verify(`Bearer ${token}x`))
      assert.isFalse(yield* auth.verify(`Bearer ${token.slice(0, -1)}`))
      assert.isFalse(yield* auth.verify(undefined))
      assert.isFalse(yield* auth.verify(""))

      // Rotation is picked up without rebuilding anything — the old token
      // stops working at once, the new one starts.
      const rotated = yield* AuthToken.rotate
      assert.notStrictEqual(rotated, token)
      assert.isFalse(yield* auth.verify(`Bearer ${token}`))
      assert.isTrue(yield* auth.verify(`Bearer ${rotated}`))

      // No token on disk: fail closed rather than accept anything.
      fs.rmSync(tokenFile)
      assert.isFalse(yield* auth.verify(`Bearer ${rotated}`))
    }).pipe(Effect.provide(AuthToken.layer.pipe(Layer.provideMerge([config, BunServices.layer]))))
  })

  it.effect("the service layer ensures the token exists at startup", () => {
    const { tokenFile, config } = withTokenDir()
    return Effect.gen(function* () {
      yield* AuthToken.AuthToken
      assert.isTrue(fs.existsSync(tokenFile))
      assert.strictEqual(fs.statSync(tokenFile).mode & 0o777, 0o600)
    }).pipe(Effect.provide(AuthToken.layer.pipe(Layer.provideMerge([config, BunServices.layer]))))
  })
})
