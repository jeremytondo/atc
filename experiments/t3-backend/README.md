# T3 backend experiment (ATC-236)

This is an external Go client for an unmodified T3 Code server. It probes the
smallest runtime/orchestration surface ATC would need without importing T3
packages, embedding a TypeScript runtime, or copying T3's Effect schemas.

The transport under `internal/t3/` implements T3's HTTP token exchange,
one-use WebSocket ticket, and the four Effect RPC JSON envelopes used by calls
and subscriptions. The harness above it keeps orchestration payloads as small
Go structs or raw JSON.

## Result

The Go integration shape is practical, but the tested stock T3 runtime is not
safe to adopt as ATC's backend yet. Discovery, thread creation, prompts,
streaming, approval and user-input responses, reconnect recovery, completion,
and failure all worked. Cancellation did not provide the lifecycle guarantees
ATC needs: a canceled command kept running, T3 projected the turn as ready /
completed rather than canceled, and interrupting a turn waiting for user input
wedged later provider work on that server instance.

See [findings.md](findings.md) for the evidence and recommendation.

## Build and test

Requirements are Go 1.26 or newer and a locally available `t3` executable. The
experiment run used the already-present T3
`0.0.34-nightly.20260823.1168`; it did not install T3 or add a T3 dependency.

```sh
cd experiments/t3-backend
make check
make build
```

The unit tests use local HTTP and WebSocket fixtures. They do not need T3,
provider credentials, or internet access.

## Run the live matrix

Use an isolated T3 base directory and disposable workspace so the experiment
does not touch a normal T3 environment or working tree:

```sh
mkdir -p /tmp/atc-236-workspace
t3 serve \
  --base-dir /tmp/atc-236-t3 \
  --host 127.0.0.1 \
  --port 17636 \
  --no-browser \
  --auto-bootstrap-project-from-cwd \
  /tmp/atc-236-workspace
```

In another terminal, create a one-use credential:

```sh
t3 auth pairing create \
  --base-dir /tmp/atc-236-t3 \
  --ttl 30m \
  --label atc-236 \
  --json
```

Pass the returned `credential` through the environment and run the probe:

```sh
ATC_T3_BOOTSTRAP_TOKEN=<credential> ./bin/atc-t3 \
  --url http://127.0.0.1:17636 \
  --project-root /tmp/atc-236-workspace \
  --scenario all
```

The credential is exchanged once and is never persisted by the experiment.
The harness prints a JSON report. Individual scenarios are `basic`,
`approval`, `input`, `cancel`, and `failure`.

