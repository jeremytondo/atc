# Unified core composition prototype

This disposable Go experiment for ATC-232 puts native chat and native TUI
execution behind one provider-neutral Thread model and a small HTTP API. It is
not production code and does not share implementation with the earlier ACP,
zmx, or status experiments.

The important boundary is visible in the packages: `domain` contains only
ATC-owned resources, `ports` defines three execution seams, `core` owns all
materialization and correlation, and protocol shapes stop inside `adapters`
and `status`. A chat Thread gets one ACP writer. A TUI Thread links to a
separate zmx-backed Terminal. Kind, agent, and canonical working directory are
immutable.

## Build and deterministic gate

```sh
cd experiments/unified-core
make check
make build
```

The gate uses subprocess and execution-seam fakes; it never contacts a real
provider or the user's zmx namespace. It covers the behavior matrix described
in `findings.md`.

## Run locally

Install Go, `codex`, `claude`, and `zmx`, then run:

```sh
cd experiments/unified-core
make play
```

On the first run, the launcher opens the Codex and Claude login flows. Those
credentials, core data, zmx sessions, and logs stay isolated under `.state/`;
later runs go straight to the TUI. The launcher builds the binary, starts the
private Codex app-server and unified core, waits for both, opens `play`, and
stops only the two background processes it created when `play` exits.

The footer shows the available keys. The client retains its last snapshot while
the core is unavailable, then reconnects and catches up. Logs are under
`.state/logs/`. Ports and model policy can be overridden with the
`ATC_UNIFIED_*` variables at the top of `scripts/play.sh`.

The lower-level CLI remains deliberately thin: `api` sends an arbitrary
canonical request and `repl` offers the same operation interactively. For
example:

```sh
./bin/atc-unified api POST /v1/threads \
  '{"kind":"chat","agent":"claude","cwd":"/absolute/project"}'
./bin/atc-unified repl
```

Use `GET /v1/events` for cursor-based JSON catch-up or request
`text/event-stream` for a live stream. Raw provider evidence is absent from
every canonical response; `GET /debug/timeline` is available only with
`--debug`. The same structured timeline is atomically rebuilt at
`.state/timeline.jsonl` for reviewed runs.

## Opt-in real smoke matrix

The deterministic gate is the default. These commands explicitly opt into
installed third-party programs and isolated state:

```sh
make smoke-acp
make smoke-zmx
```

`smoke-acp` calls both official ACP v1 adapters several times to cover allow,
deny, cancellation, and exact reload, so it uses provider quota. It reuses the
private profiles initialized by `make play`. `smoke-zmx` uses temporary
directories and the `atc-unified-` prefix.

For the full reviewed matrix, keep the shared app-server and core processes in
separate terminals, then use canonical API calls only:

1. Create Claude and Codex `chat` Threads. Prompt each to perform a harmless
   file mutation that requires approval, inspect its pending requests, answer
   one allow and one deny, and verify the filesystem outcome. Stop and restart
   the core, then prompt again to exercise exact `session/load`.
2. Create one Claude and two Codex `tui` Threads and open their linked
   Terminals. Codex starts one passive per-TUI WebSocket relay; the relay learns
   the exact root from the TUI writer's own start/resume response and forwards
   only that root and correlated descendants to the shared status reducer.
   Attach with `./bin/atc-unified attach TERMINAL_ID`.
3. Capture the exact core PID when starting it in the background. Send
   `SIGKILL` only to that PID during a turn, request, and background task. The
   shared Codex app-server, zmx daemons, TUI processes, and relays must remain.
   Restart the core and inspect the failed foreground outcome, conservative
   activity, exact ACP reconnect, and cursor catch-up.
4. Cancel a foreground chat turn while a long agent-owned command runs. Verify
   that the interrupt response claims only `foreground_turn`; continue watching
   normalized events until late tool evidence settles. Clean up the test-owned
   command process explicitly if the adapter does not.
5. Exercise Claude question-before-generic-permission ordering and a background
   descendant. Exercise a Codex root that becomes idle while its correlated
   child remains active. Finally call the internal terminal cleanup operation;
   it must terminate only reachable orphans from the private namespace and
   refuse cleanup if inventory is unavailable.

Never point these commands at the user's normal `ZMX_DIR` or shared desktop
Codex control directory. Provider and zmx availability failures are expected
typed prototype diagnostics, not reasons to infer idle activity.
