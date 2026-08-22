#!/usr/bin/env bun

const forwardIndex = process.argv.indexOf("-L")
const forward = process.argv[forwardIndex + 1]
if (forwardIndex === -1 || forward === undefined) {
  process.stderr.write("fake SSH expected -L <socket:host:port>\n")
  process.exit(2)
}

const separator = forward.indexOf(":")
if (separator === -1) {
  process.stderr.write(`fake SSH received invalid forward: ${forward}\n`)
  process.exit(2)
}

Bun.serve({
  unix: forward.slice(0, separator),
  fetch: (request) =>
    new URL(request.url).pathname === "/api/v1/health"
      ? Response.json({ status: "ok" })
      : new Response("not found", { status: 404 }),
})
