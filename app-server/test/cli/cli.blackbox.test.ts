import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { afterAll, beforeAll, describe, expect, test } from "vitest"
import packageJson from "../../package.json"
import { appServerRoot, cleanupTempDirs, makeTerminal, runCli } from "../blackbox.ts"
import { startFakeZmxServer, withFakeZmxServer } from "../testLayers.ts"

// Black-box tests of the client-backed CLI commands: exact stdout, stderr,
// and exit codes, against a real `atc serve` spawned from source.
//
// Contract: API-backed commands take zero connection flags — the base URL is
// derived from the settled configuration (ATC_PORT > config.toml > default).
// Success prints the JSON payload (2-space indented) on stdout and exits 0
// with empty stderr; config/request failures print one `atc <command>:` line
// on stderr and exit 1 with empty stdout; invalid usage prints an ERROR block
// on stderr (help goes to stdout) and exits 1.

const scratch = mkdtempSync(join(tmpdir(), "atc-cli-blackbox-"))
let serverPort: number
let env: Record<string, string | undefined>
let dataRoot: string

const cli = (args: ReadonlyArray<string>, extraEnv: Record<string, string> = {}, stdin?: string) =>
  runCli([process.execPath, "src/main.ts"], args, appServerRoot, { ...env, ...extraEnv }, stdin)

let server: Awaited<ReturnType<typeof startFakeZmxServer>>

// Terminal commands run against the deterministic fake-zmx stand-in, so
// these tests need no zmx install anywhere they run.
beforeAll(async () => {
  server = await startFakeZmxServer()
  serverPort = server.port
  env = server.env
  dataRoot = server.sandbox.base
})

afterAll(async () => {
  server.proc.kill()
  await server.proc.exited
  rmSync(scratch, { recursive: true, force: true })
  cleanupTempDirs()
})

describe("atc health/version (black box)", () => {
  test("health resolves the server from ATC_PORT and exits 0", async () => {
    const result = await cli(["health"])
    expect(result.stdout).toBe('{\n  "status": "ok"\n}\n')
    expect(result.stderr).toBe("")
    expect(result.exitCode).toBe(0)
  })

  test("version prints the payload JSON and exits 0", async () => {
    const result = await cli(["version"])
    // Running from source, build metadata is deterministic dev placeholders.
    const expected = {
      version: packageJson.version,
      apiVersion: "v1",
      commit: "dev",
      builtAt: "dev",
    }
    expect(result.stdout).toBe(JSON.stringify(expected, null, 2) + "\n")
    expect(result.stderr).toBe("")
    expect(result.exitCode).toBe(0)
  })

  test("the config file supplies the port when the environment is silent", async () => {
    const configFile = join(scratch, "config.toml")
    await Bun.write(configFile, `port = ${serverPort}\n`)
    const result = await cli(["health"], { ATC_PORT: "", ATC_CONFIG: configFile })
    expect(result.stdout).toBe('{\n  "status": "ok"\n}\n')
    expect(result.exitCode).toBe(0)
  })

  test("an unreachable server is one stderr line and exit 1", async () => {
    // Port 1 is privileged and never bound by tests, so refusal is
    // deterministic — unlike a released ephemeral port, which another
    // parallel test worker could rebind.
    const result = await cli(["health"], { ATC_PORT: "1" })
    expect(result.stdout).toBe("")
    expect(result.stderr).toMatch(/^atc health: \S/)
    expect(result.stderr.trim().split("\n")).toHaveLength(1)
    expect(result.exitCode).toBe(1)
  })

  test("invalid configuration is one stderr line naming the source and exit 1", async () => {
    const result = await cli(["health"], { ATC_PORT: "70000" })
    expect(result.stdout).toBe("")
    expect(result.stderr).toMatch(/^atc health: \S/)
    expect(result.stderr).toContain("65535")
    expect(result.stderr.trim().split("\n")).toHaveLength(1)
    expect(result.exitCode).toBe(1)
  })

  test("a contract-violating response is one stderr line and exit 1", async () => {
    const misbehaving = Bun.serve({
      hostname: "127.0.0.1",
      port: 0,
      fetch: () => Response.json({ status: "degraded" }),
    })
    try {
      const result = await cli(["health"], { ATC_PORT: String(misbehaving.port) })
      expect(result.stdout).toBe("")
      expect(result.stderr).toMatch(/^atc health: \S/)
      // Schema decode errors are multi-line by default; the CLI must collapse
      // them to keep the one-line diagnostic contract.
      expect(result.stderr.trim().split("\n")).toHaveLength(1)
      expect(result.exitCode).toBe(1)
    } finally {
      await misbehaving.stop(true)
    }
  })

  test("the removed --url flag is invalid usage and exits 1", async () => {
    const result = await cli(["health", "--url", "http://127.0.0.1:1"])
    expect(result.stderr).toContain("--url")
    expect(result.exitCode).toBe(1)
  })
})

describe("atc project / atc fs (black box)", () => {
  test("the server created and migrated the database in the configured data directory", () => {
    expect(existsSync(join(dataRoot, "data", "atc", "atc.db"))).toBe(true)
  })

  test("project lifecycle: create, list, get, update, delete", async () => {
    const created = await cli(["project", "create", "--name", "BlackBox", "--directory", scratch])
    expect(created.stderr).toBe("")
    expect(created.exitCode).toBe(0)
    const project = JSON.parse(created.stdout) as { id: string; name: string }
    expect(project.name).toBe("BlackBox")

    const listed = await cli(["project", "list"])
    expect(JSON.parse(listed.stdout)).toEqual([project])

    const fetched = await cli(["project", "get", project.id])
    expect(JSON.parse(fetched.stdout)).toEqual(project)

    const updated = await cli(["project", "update", project.id, "--name", "Renamed"])
    expect((JSON.parse(updated.stdout) as { name: string }).name).toBe("Renamed")

    // Deletion requires explicit confirmation; the CLI never prompts.
    const refused = await cli(["project", "delete", project.id])
    expect(refused.stderr).toContain("--yes")
    expect(refused.exitCode).toBe(1)

    const deleted = await cli(["project", "delete", project.id, "--yes"])
    expect(deleted.stdout).toBe("")
    expect(deleted.stderr).toBe("")
    expect(deleted.exitCode).toBe(0)

    // A second delete of the same id is a real diagnostic, not an empty line.
    const gone = await cli(["project", "delete", project.id, "--yes"])
    expect(gone.stderr.trim()).toBe(`atc project delete: no project with id ${project.id}`)
    expect(gone.exitCode).toBe(1)

    const empty = await cli(["project", "list"])
    expect(JSON.parse(empty.stdout)).toEqual([])
  }, 30_000)

  test("a missing directory is one stderr line and exit 1", async () => {
    const result = await cli([
      "project",
      "create",
      "--name",
      "Nope",
      "--directory",
      join(scratch, "does-not-exist"),
    ])
    expect(result.stdout).toBe("")
    // The tagged API error surfaces as a human diagnostic, never a bare tag
    // or an empty line.
    expect(result.stderr).toMatch(/^atc project create: directory .* does not exist$/m)
    expect(result.exitCode).toBe(1)
  })

  test("terminal lifecycle: create with argv, list, get, rename, delete", async () => {
    const project = JSON.parse(
      (await cli(["project", "create", "--name", "Term", "--directory", scratch])).stdout,
    ) as { id: string }

    const created = await cli([
      "terminal",
      "create",
      "--project",
      project.id,
      "--name",
      "runner",
      "bun",
      "run",
      "dev",
    ])
    expect(created.stderr).toBe("")
    expect(created.exitCode).toBe(0)
    const terminal = JSON.parse(created.stdout) as {
      id: string
      name: string
      status: string
      command: Array<string>
    }
    expect(terminal.status).toBe("live")
    expect(terminal.name).toBe("runner")
    expect(terminal.command).toEqual(["bun", "run", "dev"])

    const listed = await cli(["terminal", "list", "--project", project.id])
    expect((JSON.parse(listed.stdout) as Array<{ id: string }>).map((t) => t.id)).toEqual([
      terminal.id,
    ])

    const fetched = await cli(["terminal", "get", terminal.id])
    expect((JSON.parse(fetched.stdout) as { id: string }).id).toBe(terminal.id)

    const renamed = await cli(["terminal", "rename", terminal.id, "--name", "logs"])
    expect((JSON.parse(renamed.stdout) as { name: string }).name).toBe("logs")

    const refused = await cli(["terminal", "delete", terminal.id])
    expect(refused.stderr).toContain("--yes")
    expect(refused.exitCode).toBe(1)

    const deleted = await cli(["terminal", "delete", terminal.id, "--yes"])
    expect(deleted.stderr).toBe("")
    expect(deleted.exitCode).toBe(0)

    const gone = await cli(["terminal", "get", terminal.id])
    expect(gone.stderr.trim()).toBe(`atc terminal get: no terminal with id ${terminal.id}`)
    expect(gone.exitCode).toBe(1)

    // Release the project for later tests.
    await cli(["project", "delete", project.id, "--yes"])
  }, 30_000)

  test("terminal attach bridges bytes and reports the ended terminal", async () => {
    const project = JSON.parse(
      (await cli(["project", "create", "--name", "Attach", "--directory", scratch])).stdout,
    ) as { id: string }
    const terminal = JSON.parse(
      (await cli(["terminal", "create", "--project", project.id])).stdout,
    ) as { id: string }

    // Piped stdio (no TTY): the attach client still bridges bytes.
    const attach = Bun.spawn([process.execPath, "src/main.ts", "terminal", "attach", terminal.id], {
      cwd: appServerRoot,
      env,
      stdin: "pipe",
      stdout: "pipe",
      stderr: "pipe",
    })
    const reader = (attach.stdout as ReadableStream<Uint8Array>).getReader()
    const decoder = new TextDecoder()
    let seen = ""
    const sawBanner = (async () => {
      while (!seen.includes("attached")) {
        const chunk = await reader.read()
        if (chunk.done) break
        seen += decoder.decode(chunk.value)
      }
    })()
    await sawBanner

    // Deleting the terminal closes the bridge with 1000 terminal_ended; the
    // CLI reports it on stderr and exits 0.
    await cli(["terminal", "delete", terminal.id, "--yes"])
    expect(await attach.exited).toBe(0)
    const stderr = await new Response(attach.stderr as ReadableStream).text()
    expect(stderr).toContain("terminal ended")

    await cli(["project", "delete", project.id, "--yes"])
  }, 30_000)

  test("fs check resolves relative paths client-side and reports health", async () => {
    const available = await cli(["fs", "check", "."])
    expect(available.exitCode).toBe(0)
    expect((JSON.parse(available.stdout) as { state: string }).state).toBe("available")

    const missing = await cli(["fs", "check", join(scratch, "does-not-exist")])
    expect(missing.exitCode).toBe(0)
    expect((JSON.parse(missing.stdout) as { state: string }).state).toBe("missing")
  }, 30_000)
})

describe("atc thread (black box)", () => {
  test("thread lifecycle: create, list, get, update, archive, unarchive, delete", async () => {
    const projectResult = await cli([
      "project",
      "create",
      "--name",
      "ThreadBox",
      "--directory",
      scratch,
    ])
    const project = JSON.parse(projectResult.stdout) as { id: string }

    const created = await cli([
      "thread",
      "create",
      "--project",
      project.id,
      "--agent",
      "codex",
      "--name",
      "First",
    ])
    expect(created.stderr).toBe("")
    expect(created.exitCode).toBe(0)
    const thread = JSON.parse(created.stdout) as {
      id: string
      agentId: string
      name: string
      activityState: string
    }
    expect(thread.agentId).toBe("codex")
    expect(thread.name).toBe("First")
    expect(thread.activityState).toBe("idle")

    // An unknown agent slug is invalid usage, rejected client-side.
    const badAgent = await cli([
      "thread",
      "create",
      "--project",
      project.id,
      "--agent",
      "gpt-marketing",
    ])
    expect(badAgent.exitCode).toBe(1)

    const listed = await cli(["thread", "list"])
    expect(JSON.parse(listed.stdout)).toEqual([thread])

    const fetched = await cli(["thread", "get", thread.id])
    expect(JSON.parse(fetched.stdout)).toEqual(thread)

    const updated = await cli(["thread", "update", thread.id, "--name", "Renamed"])
    expect((JSON.parse(updated.stdout) as { name: string }).name).toBe("Renamed")

    // Archive hides the thread from the default list and shows it under
    // --archived; unarchive restores it.
    const archived = await cli(["thread", "archive", thread.id])
    expect((JSON.parse(archived.stdout) as { archivedAt?: string }).archivedAt).toBeTruthy()
    expect(JSON.parse((await cli(["thread", "list"])).stdout)).toEqual([])
    const archivedList = await cli(["thread", "list", "--archived"])
    expect((JSON.parse(archivedList.stdout) as Array<{ id: string }>).map((t) => t.id)).toEqual([
      thread.id,
    ])
    const restored = await cli(["thread", "unarchive", thread.id])
    expect((JSON.parse(restored.stdout) as { archivedAt?: string }).archivedAt).toBeUndefined()

    // Deletion requires explicit confirmation; the CLI never prompts.
    const refused = await cli(["thread", "delete", thread.id])
    expect(refused.stderr).toContain("--yes")
    expect(refused.exitCode).toBe(1)

    const deleted = await cli(["thread", "delete", thread.id, "--yes"])
    expect(deleted.stdout).toBe("")
    expect(deleted.stderr).toBe("")
    expect(deleted.exitCode).toBe(0)

    const gone = await cli(["thread", "get", thread.id])
    expect(gone.stderr.trim()).toBe(`atc thread get: no thread with id ${thread.id}`)
    expect(gone.exitCode).toBe(1)

    await cli(["project", "delete", project.id, "--yes"])
  }, 30_000)
})

// The five stable context variables, all explicitly unset (empty means
// unset), so gateway tests never depend on the developer's environment.
const noContext = {
  ATC_ENDPOINT: "",
  ATC_PROJECT_ID: "",
  ATC_WORKSPACE_ID: "",
  ATC_THREAD_ID: "",
  ATC_TERMINAL_ID: "",
}

describe("atc api / context / capabilities (black box)", () => {
  test("api GET passes the response body through unchanged", async () => {
    const result = await cli(["api", "GET", "/api/v1/health"])
    // The server's compact JSON, undecorated (plus the trailing newline).
    expect(result.stdout).toBe('{"status":"ok"}\n')
    expect(result.stderr).toBe("")
    expect(result.exitCode).toBe(0)

    // Methods are case-insensitive.
    const lower = await cli(["api", "get", "/api/v1/health"])
    expect(lower.stdout).toBe('{"status":"ok"}\n')
    expect(lower.exitCode).toBe(0)
  })

  test("api sends JSON bodies from a file and from stdin; empty responses print nothing", async () => {
    const bodyFile = join(scratch, "create-project.json")
    await Bun.write(
      bodyFile,
      JSON.stringify({ name: "FromFile", defaultWorkingDirectory: scratch }),
    )
    const fromFile = await cli(["api", "POST", "/api/v1/projects", "--input", bodyFile])
    expect(fromFile.stderr).toBe("")
    expect(fromFile.exitCode).toBe(0)
    const fileProject = JSON.parse(fromFile.stdout) as { id: string; name: string }
    expect(fileProject.name).toBe("FromFile")

    const fromStdin = await cli(
      ["api", "POST", "/api/v1/projects", "--input", "-"],
      {},
      JSON.stringify({ name: "FromStdin", defaultWorkingDirectory: scratch }),
    )
    expect(fromStdin.exitCode).toBe(0)
    const stdinProject = JSON.parse(fromStdin.stdout) as { id: string; name: string }
    expect(stdinProject.name).toBe("FromStdin")

    // DELETE answers 204: empty stdout, empty stderr, exit 0.
    for (const id of [fileProject.id, stdinProject.id]) {
      const deleted = await cli(["api", "DELETE", `/api/v1/projects/${id}`])
      expect(deleted.stdout).toBe("")
      expect(deleted.stderr).toBe("")
      expect(deleted.exitCode).toBe(0)
    }
  }, 30_000)

  test("api reports HTTP failures with the error body on stderr and exits 1", async () => {
    const result = await cli(["api", "GET", "/api/v1/projects/nope"])
    expect(result.stdout).toBe("")
    expect(result.stderr).toContain('"_tag":"ProjectNotFound"')
    expect(result.stderr).toContain("atc api: server returned HTTP 404")
    expect(result.exitCode).toBe(1)
  })

  test("api transport failures are one stderr line and exit 1", async () => {
    const result = await cli(["api", "GET", "/api/v1/health"], { ATC_PORT: "1" })
    expect(result.stdout).toBe("")
    expect(result.stderr).toMatch(/^atc api: \S/)
    expect(result.stderr.trim().split("\n")).toHaveLength(1)
    expect(result.exitCode).toBe(1)
  })

  test("api rejects paths that are not server-relative or would re-target the origin", async () => {
    // Absolute URLs, network-path references (//host), and the backslash
    // variant the URL parser treats as // must all fail before any request.
    for (const bad of [
      "http://evil.example/api/v1/health",
      "//evil.example/api/v1/health",
      "/\\evil.example/api/v1/health",
      "//",
    ]) {
      const result = await cli(["api", "GET", bad])
      expect(result.stdout).toBe("")
      expect(result.stderr).toMatch(/^atc api: path must be server-relative/)
      expect(result.exitCode).toBe(1)
    }
  })

  test("api rejects unsupported methods as invalid usage", async () => {
    const result = await cli(["api", "FROB", "/api/v1/health"])
    expect(result.stderr).toContain("method must be one of GET, POST, PUT, PATCH, DELETE")
    expect(result.exitCode).toBe(1)
  })

  test("an ATC_ENDPOINT path prefix is preserved when joining", async () => {
    // A stand-in server that echoes the request path proves the join keeps
    // the prefix instead of discarding it the way `new URL("/x", base)` would.
    const echo = Bun.serve({
      hostname: "127.0.0.1",
      port: 0,
      fetch: (request) => Response.json({ path: new URL(request.url).pathname }),
    })
    try {
      const result = await cli(["api", "GET", "/api/v1/health"], {
        ATC_ENDPOINT: `http://127.0.0.1:${echo.port}/prefix`,
      })
      expect(JSON.parse(result.stdout)).toEqual({ path: "/prefix/api/v1/health" })
      expect(result.exitCode).toBe(0)
    } finally {
      await echo.stop(true)
    }
  })

  test("ATC_ENDPOINT beats the configured port at the resolution seam", async () => {
    // ATC_PORT points nowhere; the endpoint carries the real server.
    const viaEndpoint = await cli(["health"], {
      ATC_PORT: "1",
      ATC_ENDPOINT: `http://127.0.0.1:${serverPort}`,
    })
    expect(viaEndpoint.stdout).toBe('{\n  "status": "ok"\n}\n')
    expect(viaEndpoint.exitCode).toBe(0)

    const malformed = await cli(["health"], { ATC_ENDPOINT: "not a url" })
    expect(malformed.stderr).toMatch(/^atc health: ATC_ENDPOINT: /)
    expect(malformed.exitCode).toBe(1)
  })

  test("context reports present variables only, in vocabulary order", async () => {
    const absent = await cli(["context"], noContext)
    expect(absent.stdout).toBe("")
    expect(absent.stderr).toBe("")
    expect(absent.exitCode).toBe(0)

    const absentJson = await cli(["context", "--json"], noContext)
    expect(absentJson.stdout).toBe("{}\n")
    expect(absentJson.exitCode).toBe(0)

    const partial = await cli(["context"], {
      ...noContext,
      ATC_TERMINAL_ID: "t-1",
      ATC_PROJECT_ID: "p-1",
    })
    expect(partial.stdout).toBe("ATC_PROJECT_ID=p-1\nATC_TERMINAL_ID=t-1\n")
    expect(partial.exitCode).toBe(0)

    const full = await cli(["context", "--json"], {
      ...noContext,
      ATC_ENDPOINT: `http://127.0.0.1:${serverPort}`,
      ATC_PROJECT_ID: "p-1",
      ATC_WORKSPACE_ID: "w-1",
      ATC_THREAD_ID: "th-1",
      ATC_TERMINAL_ID: "t-1",
    })
    expect(JSON.parse(full.stdout)).toEqual({
      ATC_ENDPOINT: `http://127.0.0.1:${serverPort}`,
      ATC_PROJECT_ID: "p-1",
      ATC_WORKSPACE_ID: "w-1",
      ATC_THREAD_ID: "th-1",
      ATC_TERMINAL_ID: "t-1",
    })
    expect(full.exitCode).toBe(0)
  })

  test("capabilities --json is stable, versioned, machine-readable output", async () => {
    const result = await cli(["capabilities", "--json"])
    expect(result.stderr).toBe("")
    expect(result.exitCode).toBe(0)
    // The complete v1 shape, pinned: any change here is a compatibility
    // decision and must either be additive or bump capabilitiesVersion.
    expect(JSON.parse(result.stdout)).toEqual({
      capabilitiesVersion: 1,
      api: {
        command: "atc api <method> <path> [--input <file|->]",
        description:
          "Complete access to the canonical App Server HTTP API: every operation via GET, POST, PUT, PATCH, or DELETE, JSON body from a file or stdin, the raw JSON response on stdout.",
        example: "atc api GET /api/v1/projects",
      },
      openapi: {
        path: "/openapi.json",
        description:
          "The full OpenAPI document (every operation and schema), served by the App Server.",
        example: "atc api GET /openapi.json",
      },
      context: {
        command: "atc context --json",
        description: "The ATC_* context variables present in this process.",
        variables: [
          "ATC_ENDPOINT",
          "ATC_PROJECT_ID",
          "ATC_WORKSPACE_ID",
          "ATC_THREAD_ID",
          "ATC_TERMINAL_ID",
        ],
      },
      workflows: [
        { command: "atc serve", description: "Run the App Server in the foreground." },
        {
          command: "atc project create --name <name> --directory <dir>",
          description: "Create a project (relative directories resolve against the caller's cwd).",
        },
        {
          command: "atc terminal create --project <id> [command...]",
          description: "Create a durable terminal and start its session immediately.",
        },
        {
          command: "atc terminal attach <terminal-id>",
          description: "Attach the local TTY to a live terminal session (detach with Ctrl-]).",
        },
      ],
    })

    // The human rendering carries the same content, one line per capability.
    const text = await cli(["capabilities"])
    expect(text.exitCode).toBe(0)
    expect(text.stdout).toContain("atc api")
  })

  test("launched terminal sessions receive this server's ATC_ENDPOINT", async () => {
    const project = JSON.parse(
      (await cli(["project", "create", "--name", "Ctx", "--directory", scratch])).stdout,
    ) as { id: string }
    const terminal = JSON.parse(
      (await cli(["terminal", "create", "--project", project.id])).stdout,
    ) as { id: string }

    // The fake-zmx session record captures the environment the adapter
    // launched with; the session name is atc-<uuid without dashes>.
    const sessionFile = join(server.sandbox.stateDir, `atc-${terminal.id.replaceAll("-", "")}`)
    const record = JSON.parse(readFileSync(sessionFile, "utf8")) as {
      env: Record<string, string | undefined>
    }
    expect(record.env["ATC_ENDPOINT"]).toBe(`http://127.0.0.1:${serverPort}`)

    await cli(["terminal", "delete", terminal.id, "--yes"])
    await cli(["project", "delete", project.id, "--yes"])
  }, 30_000)

  test("serve --port folds the effective port into the injected ATC_ENDPOINT", async () => {
    // ATC_PORT deliberately disagrees with the --port flag the server is
    // launched with: the injected endpoint must reflect the flag (the port
    // actually being served), proving the flag is folded back into the
    // settled AppConfig rather than read from the environment level.
    await withFakeZmxServer(
      async (flagged) => {
        const terminal = await makeTerminal(flagged.base)
        const sessionFile = join(
          flagged.sandbox.stateDir,
          `atc-${(terminal["id"] as string).replaceAll("-", "")}`,
        )
        const record = JSON.parse(readFileSync(sessionFile, "utf8")) as {
          env: Record<string, string | undefined>
        }
        expect(record.env["ATC_ENDPOINT"]).toBe(flagged.base)
      },
      { ATC_PORT: "1" },
    )
  }, 30_000)
})
