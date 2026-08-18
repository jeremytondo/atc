import { mkdtempSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { describe, expect, test } from "vitest"
import { appServerRoot, runCli, trackTempDir } from "../blackbox.ts"
import { withFakeZmxServer } from "../testLayers.ts"

// Live Thread runtime smoke (opt-in: mise run test:smoke): the whole stack
// against real Claude Code — a real `atc serve` from source, `atc thread
// prompt --follow` streaming the turn to its end, then the transcript and
// the queue read back through the CLI. Needs an installed, authenticated
// Claude Code; runs one real (cheap) turn from a directory outside any
// repository. Codex is exercised at the adapter level (codexAdapter.smoke);
// the runtime is provider-neutral above the seam.

const enabled = process.env["ATC_SMOKE"] === "1"

describe.skipIf(!enabled)("live thread runtime smoke (opt-in)", () => {
  test("prompt --follow runs a real Claude turn end to end; the transcript reads back", async () => {
    const cwd = trackTempDir(mkdtempSync(join(tmpdir(), "atc-runtime-smoke-")))
    await withFakeZmxServer(async (server) => {
      const cli = (args: ReadonlyArray<string>) =>
        runCli([process.execPath, "src/main.ts"], args, appServerRoot, server.env)
      const project = JSON.parse(
        (await cli(["project", "create", "--name", "Smoke", "--directory", cwd])).stdout,
      ) as { id: string }
      const thread = JSON.parse(
        (
          await cli([
            "thread",
            "create",
            "--project",
            project.id,
            "--agent",
            "claude-code",
            "--directory",
            cwd,
          ])
        ).stdout,
      ) as { id: string }

      const followed = await cli([
        "thread",
        "prompt",
        thread.id,
        "Reply with exactly: SMOKE-OK",
        "--follow",
      ])
      // Real Claude Code may print provider notices on stderr; the exit
      // code is the verdict (stderr rides along in the failure message).
      expect(followed.exitCode, followed.stderr).toBe(0)
      const events = followed.stdout
        .trim()
        .split("\n")
        .map((line) => JSON.parse(line)) as Array<{
        type: string
        item?: { type: string; text?: string }
      }>
      expect(events[0]?.type).toBe("queue.updated")
      expect(events.at(-1)?.type).toBe("turn.completed")
      // The prompt landed as the turn's first item; the reply streamed and
      // completed with the requested text.
      expect(events.some((event) => event.item?.type === "userMessage")).toBe(true)
      expect(events.some((event) => event.type === "text.delta")).toBe(true)
      expect(
        events.some(
          (event) =>
            event.type === "item.completed" &&
            event.item?.type === "assistantText" &&
            (event.item.text ?? "").includes("SMOKE-OK"),
        ),
      ).toBe(true)

      const transcript = JSON.parse((await cli(["thread", "transcript", thread.id])).stdout) as {
        items: Array<{ type: string }>
        turns: Array<{ status: string }>
      }
      expect(transcript.turns.map((turn) => turn.status)).toEqual(["completed"])
      expect(transcript.items[0]?.type).toBe("userMessage")
      expect(JSON.parse((await cli(["thread", "queue", thread.id])).stdout)).toEqual([])
    })
  }, 240_000)
})
