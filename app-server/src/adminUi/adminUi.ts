// The embedded admin UI (ATC-126): serves the prerendered SvelteKit build
// from web/build on a GET wildcard route. Invariants:
//
// - Every page is prerendered to a real HTML file, so serving is a pure
//   path -> file lookup with no SPA fallback; unknown paths are 404s, and
//   trailing-slash variants redirect to the canonical path so the pages'
//   relative asset URLs resolve.
// - A compiled binary serves the files embedded via manifest.generated.ts
//   (imported dynamically so nothing outside a compiled run ever parses it);
//   a source run lists web/build relative to this module. Neither path
//   depends on the working directory.
// - The wildcard never shadows the API: the router matches static routes
//   (/api/v1/*, /openapi.json) before wildcards, and the trust guard in
//   localTrust.ts applies to these routes like any other.
import { fileURLToPath } from "node:url"
import { join } from "node:path"
import { Effect } from "effect"
import { HttpRouter, HttpServerRequest, HttpServerResponse } from "effect/unstable/http"
import { buildInfo } from "../platform/buildInfo.ts"

/** The request path a file in web/build is served under ("index.html" -> "/"). */
export const routeForFile = (file: string): string => {
  if (file === "index.html") return "/"
  if (file.endsWith("/index.html")) return `/${file.slice(0, -"/index.html".length)}`
  if (file.endsWith(".html")) return `/${file.slice(0, -".html".length)}`
  return `/${file}`
}

type Asset = {
  readonly filePath: string
  readonly cacheControl: string
}

const toAssetMap = (files: Iterable<readonly [string, string]>): ReadonlyMap<string, Asset> => {
  const map = new Map<string, Asset>()
  for (const [file, filePath] of files) {
    map.set(routeForFile(file), {
      filePath,
      // Hashed build output is immutable by construction; everything else
      // (the HTML shells, favicon) must revalidate so UI updates land.
      cacheControl: file.startsWith("_app/immutable/")
        ? "public, max-age=31536000, immutable"
        : "no-cache",
    })
  }
  return map
}

const listBuildDir = async (): Promise<Array<readonly [string, string]>> => {
  const buildDir = fileURLToPath(new URL("../../web/build/", import.meta.url))
  const files: Array<readonly [string, string]> = []
  try {
    for await (const file of new Bun.Glob("**/*").scan({ cwd: buildDir, onlyFiles: true })) {
      files.push([file, join(buildDir, file)])
    }
  } catch {
    // No build output (fresh checkout of a broken state, or web/build was
    // deleted). Serve 404s rather than memoizing a rejection into a
    // permanent 500; the warning below points at the fix.
  }
  return files
}

const loadAssets = (): Promise<ReadonlyMap<string, Asset>> =>
  buildInfo.commit === "dev"
    ? listBuildDir().then(toAssetMap)
    : import("./manifest.generated.ts").then((manifest) => toAssetMap(manifest.assets))

// The asset map is built once at layer build — the content is fixed for the
// process lifetime in both modes (embedded files in a compiled binary, the
// committed build on a source run) — and each asset's bytes are read once,
// on first request, then served from memory.
export const route = HttpRouter.use((router) =>
  Effect.gen(function* () {
    const assets = yield* Effect.promise(loadAssets)
    if (assets.size === 0) {
      yield* Effect.logWarning("admin UI assets missing — run `mise run web:build`")
    }
    const loaded = new Map<string, { readonly bytes: Uint8Array; readonly contentType: string }>()
    yield* router.add(
      "GET",
      "*",
      Effect.gen(function* () {
        const request = yield* HttpServerRequest.HttpServerRequest
        const rawPath = new URL(request.url, "http://localhost").pathname
        const pathname = yield* Effect.try(() => decodeURIComponent(rawPath)).pipe(
          Effect.orElseSucceed(() => rawPath),
        )
        const asset = assets.get(pathname)
        if (asset === undefined) {
          // "/docs/" -> "/docs": the prerendered pages use relative asset
          // URLs, which only resolve from the canonical (slash-less) path.
          const trimmed = pathname.length > 1 && pathname.endsWith("/") ? pathname.slice(0, -1) : ""
          if (assets.has(trimmed)) return HttpServerResponse.redirect(trimmed, { status: 308 })
          return HttpServerResponse.empty({ status: 404 })
        }
        const entry =
          loaded.get(pathname) ??
          (yield* Effect.promise(async () => {
            const file = Bun.file(asset.filePath)
            const read = {
              bytes: await file.bytes(),
              contentType: file.type === "" ? "application/octet-stream" : file.type,
            }
            loaded.set(pathname, read)
            return read
          }))
        return HttpServerResponse.uint8Array(entry.bytes, {
          contentType: entry.contentType,
          headers: { "cache-control": asset.cacheControl },
        })
      }),
    )
  }),
)
