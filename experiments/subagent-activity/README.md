# Subagent activity probes (ATC-158)

Recording probes for aggregate thread activity across background subagents.
They are experiments, not production code. See [`findings.md`](findings.md)
for the reviewed evidence; raw recordings sit next to each probe as
`*.jsonl`.

Prerequisites: an authenticated Claude Code install and an authenticated
`codex` CLI. Each probe run drives real model turns (small ones).

```sh
cd experiments/subagent-activity
bun install
bun claude/record.ts          # hook payloads around background work
bun codex/record.ts           # descendant thread broadcasts + thread/list
bun codex/read-probe.ts <rootId> <childId>   # token-free thread/read check
bun codex/reconcile-probe.ts  # fresh-connection loaded/list + thread/read
```
