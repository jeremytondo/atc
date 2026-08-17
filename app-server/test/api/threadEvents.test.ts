import { assert, describe, it } from "@effect/vitest"
import { BunServices } from "@effect/platform-bun"
import { Effect, Layer } from "effect"
import * as Server from "../../src/server.ts"
import { TestBuildInfoLayer } from "../testBuildInfo.ts"
import { makeTestServiceLayers, startServer, TEST_AUTH_TOKEN } from "../testLayers.ts"

// The multi-client, remote-client proof (ATC-193): two independent clients
// attached to one thread's event stream — one a plain loopback client, one
// the shape of a remote client (a non-loopback Host plus the bearer token,
// the same rule localTrust.test.ts pins) — see the same events in the same
// order, and a prompt sent from either shows up in both. Real listener: the
// trust guard and the SSE framing sit on the HTTP layer.

/**
 * Read `data:` frames off an SSE response until `until` matches one —
 * bounded, so a missing frame fails naming what arrived instead of hanging
 * on heartbeats.
 */
const readFrames = async (
  response: Response,
  until: (frame: Record<string, unknown>) => boolean,
  deadlineMillis = 10_000,
): Promise<Array<Record<string, unknown>>> => {
  const reader = response.body!.getReader()
  const decoder = new TextDecoder()
  let text = ""
  const frames: Array<Record<string, unknown>> = []
  const deadline = Date.now() + deadlineMillis
  for (;;) {
    const remaining = deadline - Date.now()
    if (remaining <= 0) {
      await reader.cancel()
      throw new Error(`no matching frame within ${deadlineMillis}ms; saw ${JSON.stringify(frames)}`)
    }
    const chunk = await Promise.race([
      reader.read(),
      new Promise<never>((_, reject) =>
        setTimeout(
          () => reject(new Error(`SSE read stalled; saw ${JSON.stringify(frames)}`)),
          remaining,
        ),
      ),
    ])
    if (chunk.done) break
    text += decoder.decode(chunk.value, { stream: true })
    // Stop at the matching frame itself, whatever else the chunk carried.
    let end = text.indexOf("\n\n")
    let stop = false
    while (end >= 0 && !stop) {
      const raw = text.slice(0, end)
      text = text.slice(end + 2)
      if (raw.startsWith("data: ")) {
        const frame = JSON.parse(raw.slice("data: ".length)) as Record<string, unknown>
        frames.push(frame)
        stop = until(frame)
      }
      end = text.indexOf("\n\n")
    }
    if (stop) break
  }
  await reader.cancel()
  return frames
}

describe("thread event stream: two clients, one remote", () => {
  it.live("both clients see the same events in the same order, whoever prompted", () =>
    Effect.gen(function* () {
      const kit = makeTestServiceLayers()
      const { base: local } = yield* startServer(
        Server.layer({ port: 0 }).pipe(Layer.provide([TestBuildInfoLayer, kit.layer])),
      )
      const port = new URL(local).port
      const remoteHeaders = {
        Host: `workstation.tailnet:${port}`,
        Authorization: `Bearer ${TEST_AUTH_TOKEN}`,
      }
      const json = <A>(response: Promise<Response>) => response.then((r) => r.json() as Promise<A>)
      const post = (path: string, body: unknown, headers: Record<string, string> = {}) =>
        fetch(`${local}${path}`, {
          method: "POST",
          headers: { "content-type": "application/json", ...headers },
          body: JSON.stringify(body),
        })

      const project = yield* Effect.promise(() =>
        json<{ id: string }>(
          post("/api/v1/projects", { name: "TwoClients", defaultWorkingDirectory: "/tmp" }),
        ),
      )
      const thread = yield* Effect.promise(() =>
        json<{ id: string }>(post("/api/v1/threads", { projectId: project.id, agentId: "codex" })),
      )
      const eventsPath = `${local}/api/v1/threads/${thread.id}/events`

      // A remote client without the token is refused; with it, it streams.
      const refused = yield* Effect.promise(() =>
        fetch(eventsPath, { headers: { Host: `workstation.tailnet:${port}` } }),
      )
      assert.strictEqual(refused.status, 403)
      const [localStream, remoteStream] = yield* Effect.promise(() =>
        Promise.all([
          fetch(eventsPath, { headers: { accept: "text/event-stream" } }),
          fetch(eventsPath, { headers: { accept: "text/event-stream", ...remoteHeaders } }),
        ]),
      )
      assert.strictEqual(localStream.status, 200)
      assert.strictEqual(remoteStream.status, 200)

      // The remote client prompts; the local one prompts again (queued).
      const first = yield* Effect.promise(() =>
        json<{ promptId: string; turnId?: string }>(
          post(`/api/v1/threads/${thread.id}/prompt`, { prompt: "from remote" }, remoteHeaders),
        ),
      )
      assert.isString(first.turnId)
      const second = yield* Effect.promise(() =>
        json<{ promptId: string; turnId?: string }>(
          post(`/api/v1/threads/${thread.id}/prompt`, { prompt: "from local" }),
        ),
      )
      assert.isUndefined(second.turnId)
      // The fake provider finishes the first turn; the queued prompt runs.
      const session = [...kit.fakeAgents.codex.sessions.values()].find((entry) =>
        entry.inputs.includes("from remote"),
      )
      assert.isDefined(session, "the fake adapter recorded no session for the remote prompt")
      kit.fakeAgents.codex.completeTurn(session.providerSessionId, "completed")

      const untilSecondTurn = (frame: Record<string, unknown>) =>
        frame["type"] === "turn.started" &&
        (frame["turn"] as { promptId?: string }).promptId === second.promptId
      const [localFrames, remoteFrames] = yield* Effect.promise(() =>
        Promise.all([
          readFrames(localStream, untilSecondTurn),
          readFrames(remoteStream, untilSecondTurn),
        ]),
      )
      // Identical, in order — the server-side copy is the one conversation.
      assert.deepStrictEqual(localFrames, remoteFrames)
      const types = localFrames.map((frame) => frame["type"])
      assert.include(types, "turn.started")
      assert.include(types, "turn.completed")
      assert.include(types, "item.completed")
      assert.strictEqual(types.filter((type) => type === "turn.started").length, 2)
      const seqs = localFrames.flatMap((frame) =>
        typeof frame["seq"] === "number" ? [frame["seq"]] : [],
      )
      assert.deepStrictEqual(
        seqs,
        seqs.toSorted((left, right) => left - right),
      )
    }).pipe(Effect.scoped, Effect.provide(BunServices.layer)),
  )
})
