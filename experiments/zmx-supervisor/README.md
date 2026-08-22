# zmx terminal-agent experiment (ATC-230 / ATC-231)

This is an isolated Go prototype for testing zmx as the durable owner of ATC
terminal-backed sessions. It does not import or modify the current app server.

ATC-231 extends the same harness with an isolated `internal/agentstatus/`
layer. Claude sessions receive launch-time lifecycle hooks. Codex sessions run
through a private app-server bridge that passively records the remote TUI's
structured thread status. The supervisor consumes only normalized agent status
and evidence.

The zmx-specific implementation is confined to `internal/terminal/`. The
supervisor above it deals only in terminal sessions, persisted metadata, exit
evidence, and the normalized states `running`, `exited`, `missing`,
`disconnected`, and `stale`.

## Build and test

Requirements are Go 1.26 or newer and zmx 0.6.0 on `PATH`. Real provider
workloads also require their corresponding CLI; the Codex bridge was validated
with Codex 0.149.0 and requires `app-server --listen` plus TUI `--remote`
support.

```sh
cd experiments/zmx-supervisor
make check
make build
```

The normal suite uses an in-process fake terminal boundary and does not touch
the user's zmx sessions. The opt-in smoke test uses a throwaway private
`ZMX_DIR` and cleans up its exact session:

```sh
make test-zmx
```

## Run the harness

```sh
./bin/atc-zmx --cwd /path/to/project
```

The default state is under `<cwd>/.atc-zmx/`; its `zmx/` child is the private
socket directory. `--state-dir` selects another location. Every zmx process is
given that explicit `ZMX_DIR`, and inherited `ZMX_SESSION` and
`ZMX_SESSION_PREFIX` values are removed. The experiment therefore cannot see
or operate on ordinary zmx sessions or ATC's real terminal namespace.

The REPL supports:

```text
create NAME shell
create NAME process COMMAND [ARGS]
create NAME codex [ARGS]
create NAME claude [ARGS]
list
status NAME
transitions NAME
send NAME TEXT
send-raw NAME ESCAPED
history NAME [LINES]
attach NAME
stop NAME
cleanup
crash
quit
```

`attach` hands the current TTY to zmx. Press Ctrl-\ to detach and return to the
REPL without ending the session. `send` appends a carriage return; `send-raw`
accepts Go-style escapes such as `\x03`. `history` reads scrollback without
attaching. `status` includes the currently authoritative normalized agent
observation, and `transitions` prints its persisted evidence timeline. All
commands also work as one-shot invocations, which makes restart tests
straightforward:

```sh
./bin/atc-zmx --state-dir /tmp/atc-zmx-demo create demo shell
./bin/atc-zmx --state-dir /tmp/atc-zmx-demo send demo echo recovered
./bin/atc-zmx --state-dir /tmp/atc-zmx-demo history demo 20
./bin/atc-zmx --state-dir /tmp/atc-zmx-demo stop demo
./bin/atc-zmx --state-dir /tmp/atc-zmx-demo cleanup
```

Use `crash` from the REPL to exit the Go host with status 86 and deliberately
skip cleanup. Start it again with the same state directory and run `recover`;
surviving zmx sessions retain the same daemon PID and can be attached again.

## Recovery model

Each managed command is launched through a tiny copy of this executable. zmx
still owns the PTY and durable session; the wrapper only writes an atomic exit
marker beside the supervisor metadata. That evidence is what makes the
important distinctions robust:

- A reachable inventory entry is `running`.
- An inventory entry with `err=` is `disconnected`; it is never killed or
  treated as absent automatically.
- An absent entry with a completed child marker is `exited`.
- An absent entry without exit evidence is `missing` during a grace period,
  then `stale`.
- An unmanaged reachable entry in the experiment's private namespace is a
  stale orphan.

`cleanup` requires a complete inventory. It kills reachable orphans and
forgets only exited or stale managed records. It preserves running, missing,
and disconnected sessions. `stop` records intent before asking zmx to kill the
session, so a deliberate termination remains distinguishable even if the
child cannot finish its exit marker.

See [findings.md](findings.md) for the exercised matrix and conclusions.

## Agent status model

For running Claude and Codex workloads, the status layer selects exactly one
source:

1. A recognized structured lifecycle signal. The prototype wires real Claude
   hooks into the private state directory. Its Codex bridge launches a private
   app-server, connects the zmx-backed TUI with `--remote`, and observes that
   same server without sending turns or answering requests.
2. Process evidence, which reports `unknown` while the child is running.

Terminal-screen inference is intentionally not part of the status path. Live
Claude and Codex testing showed that prompts, trust dialogs, question dialogs,
and transient working screens reuse the same glyphs and text. Polling can also
miss short turns entirely, so matching screen contents would produce false
confidence rather than a reliable fallback.

The per-session Codex app-server exists only to isolate this status experiment.
The unified ATC core should own one long-lived shared app-server per profile and
attach every Codex TUI to it; the structured event and correlation model is the
same.

Once the child exits, its exit marker overrides stale provider evidence and
produces `completed` or `failed`. Missing, stale, and disconnected sessions are
`unavailable`. Every observed normalized transition records its source, rule,
detail, timestamp, and raw evidence under
`<state-dir>/agent-status/<session-id>/`.

See [status-findings.md](status-findings.md) for the ATC-231 scenarios,
limitations, and recommendation.
