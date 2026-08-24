# Terminal-native ATC client

Historical reference for the OpenTUI client prototype that lived at
`atc-tui/` before the ATC-243 reset. The complete source and tests are preserved
at
[`legacy-product-2026-08/atc-tui`](https://github.com/jeremytondo/atc/tree/legacy-product-2026-08/atc-tui).

The prototype proved that a terminal-native client can act as an ATC control
surface without becoming a second control plane. The server continued to own
Projects, Threads, Terminals, and activity state; the client rendered that
state, invoked server operations, and attached the caller to a server-selected
Terminal.

## What it proved

- One persistent terminal UI can navigate active Threads, archived Threads,
  and Projects while retaining selection and interaction state across server
  refreshes.
- A resource-change event stream can remain deliberately small: events trigger
  coalesced snapshot refetches instead of replicating domain state into the
  client.
- OpenTUI can relinquish the caller's real TTY while zmx owns it, then restore
  the existing renderer and manager state after the attachment ends.
- Local and remote attachment can share the same application transition. The
  local path attaches directly to ATC's zmx namespace; the remote path hands
  the TTY to an interactive SSH client.
- A remote App Server can remain loopback-only. A supervised SSH forward used
  a private Unix socket for control traffic, while a separate interactive SSH
  process handled terminal attachment without reserving a laptop TCP port.
- Losing the interactive SSH connection does not end the zmx session. The
  client can retry the same Terminal with backoff and receive zmx's repaint on
  reconnection.
- Thread activity, unread state, and needs-input state are enough to prioritize
  a compact terminal-native work queue without rendering full conversations.

## Architecture

The prototype was a standalone Bun executable built with Effect and OpenTUI.
Its main pieces were:

- an App Server facade over the generated HTTP client, with concurrent snapshot
  reads for Projects, Threads, and Agents;
- a reconnecting SSE consumer with heartbeat detection, bounded backoff, and a
  sliding refresh queue that coalesced bursts of resource-change events;
- a scoped application coordinator that retained the renderer, navigation
  state, selection, and current attachment across refreshes;
- a persistent OpenTUI frame whose panels implemented list, form, directory
  completion, and destructive-action flows;
- a terminal-attachment seam that released the renderer before running local
  zmx or remote `atc terminal attach`, then reacquired it afterward; and
- a remote transport that supervised SSH forwarding independently from the
  interactive SSH process that owned the TTY.

## Why only the findings remain on main

The prototype imported the old TypeScript App Server's contract, generated
client, subprocess service, and custom lint rules directly. It was therefore
not a self-contained client and cannot remain runnable after that server is
removed. Making it independent would turn superseded code into a maintained
surface without answering a question needed by ATC-243.

Treat these findings as evidence, not as a production design. To inspect or run
the historical implementation, create a working commit from the archive tag:

```sh
jj new legacy-product-2026-08
mise install
mise run -C atc-tui check
```
