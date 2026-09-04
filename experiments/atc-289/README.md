# ATC-289 live smoke check

`scripts/atc-289` is the manual gate for adopting a T3 Code nightly once ATC
can start T3 threads. It is not part of `mise test`: it needs an installed T3
runtime, a provider the private environment can run, and it spends a real
model turn.

The wrapper:

1. builds `atc` from this checkout;
2. starts a private T3 Code environment from the installed runtime under
   `${T3CODE_HOME:-~/.t3}` — its own base dir, its own port, the workspace
   auto-bootstrapped as a project, and the installed CLI linked in where a
   service-run home keeps it so ATC's zero-touch pairing works unchanged;
3. starts an ATC server with private XDG state pointed at that environment,
   registers the workspace as a project, and waits for the T3 Code
   Integration to pair and connect;
4. runs `atc thread create` with the given agent, model, options, and prompt,
   then polls `atc thread get` until the first turn completes, fails, or the
   timeout passes;
5. on exit revokes the session ATC paired, stops both servers, and removes the
   private state (a `--project-root` you supplied is kept).

```sh
scripts/atc-289 --model gpt-5.6-sol --option reasoningEffort=low
scripts/atc-289 --agent claudeAgent --model claude-sonnet-5 --timeout 300
```

PASS means the thread appeared in ATC with status `working` and a running
provisional turn, T3 accepted the command, and T3's own events drove the same
thread to a completed first turn. Anything else prints the final
`atc thread get` and the log locations.

The deterministic coverage of the same path — command payload, ATC-owned
defaults, the shell event binding the turn, every refusal, the scope re-pair,
and the multiplexed socket — lives in the package tests under
`internal/integrations/t3code`, `internal/server`, and `cmd/atc`, against the
in-process fake in `internal/integrations/t3code/t3codetest`.
