# ATC Agent Instructions

## Current Rebuild Notes

ATC is being rebuilt in Go under [ATC-243](https://linear.app/elevenideas/issue/ATC-243). The active tree is
the rebuild's scaffold: a single Go module at the repository root with one
entrypoint, `cmd/atc`, plus the research archive under `experiments/`. The
superseded product is preserved at the `legacy-product-2026-08` tag.

## Core Principles

It's important we maintain the following core principles as we iterate on ATC:

- Performance
- Reliability
- Simplicity
- User experience

If a tradeoff is required, choose correctness and robustness over short-term
convenience.

## Recorded Decisions

Recorded decisions and rules capture the
best understanding at the time, not permanent truth. When the work surfaces
evidence that a prior decision is wrong or improvable, challenge it openly
and propose amending the record; do not follow it blindly or deviate from it
silently.

## Maintainability

Long-term maintainability is a core priority. Prefer shared, plainly named
logic over duplication, and change an existing design when that produces a
simpler system. Code should be easy to
understand, work with, and test.

## Documentation

Documentation defaults to the code. A module header should record its
responsibility and non-obvious invariants; comments should explain surprising
constraints, not narrate control flow.

Keep this file limited to facts and priorities that cannot be learned by
reading the implementation. READMEs cover how to run a thing, where its data
lives, and which task to use. They should point to generated references and
`--help` rather than enumerate details that can drift.

## Testing

- Use the stdlib `testing` package, with go-cmp for structural diffs
  (`cmp.Diff` against a full want value; see `internal/config` for the
  pattern).
- No assertion or mocking frameworks. Test doubles are hand-written fakes
  behind the interfaces the code already defines.
- The race detector stays on (`mise test` runs it).
- Use table tests where they clarify a set of cases, not as ritual.
- Tests are serial by default. Add `t.Parallel()` deliberately, when a
  package's tests become slow enough to pay for it — cross-package
  parallelism from `go test ./...` already carries the wall-clock win for a
  tree of many small packages.

## Experiments and Reference Source

Everything under `experiments/` is historical research material, not
production source:

- Read findings before code.
- Treat findings as evidence, not decisions
- Do not import, extend, or make production depend on experiment code.
- Do not rewrite old findings. Capture new evidence in a new experiment.

Reference checkouts under `repos/`, when present, are read-only. Never edit
them, import from them, or copy them wholesale into the product.

## Safety

- Never touch the developer's real zmx sessions or shared Codex server while
  testing. Use private, explicitly scoped state directories.
- Never kill processes by name or pattern. Kill only a PID captured when
  starting a process for the current task.
- Preserve unrelated working-copy changes.

## Source Control

This is a Jujutsu repository. Do not use `git add`, `git commit`,
`git stash`, or `git checkout`.

When a logical step passes its checks, checkpoint it with
`jj describe -m "<message>"` followed by `jj new`. If a code change breaks
the build, use `jj undo` before trying again. Use a jj bookmark when pushing
work to the Git remote.

Run `gh` commands as standalone shell commands; do not combine them with
other commands.

## Project Tools

When working in Linear, always use the ATC team:
<https://linear.app/elevenideas/team/ATC>.

When delegating work, select a cost-appropriate model and review its output.
