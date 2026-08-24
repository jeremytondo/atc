# ATC Agent Instructions

ATC is in a deliberate repository reset for the Go rebuild described by
[ATC-243](https://linear.app/elevenideas/issue/ATC-243). The active tree
contains the research archive and project guidance, but no current product
implementation. The superseded product is preserved at the
`legacy-product-2026-08` tag.

ATC-243 is the source of truth for the rebuild. Do not restore or extend the
archived TypeScript App Server, macOS app, packages, workflows, or release
tooling in the active tree. Compatibility with legacy state and configuration
is a separate implementation decision tracked by ATC-245.

## Core Priorities

- Performance
- Reliability
- Simplicity
- User experience

If a tradeoff is required, choose correctness and robustness over short-term
convenience.

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

## Experiments and Reference Source

Everything under `experiments/` is historical research material, not
production source:

- Read findings before code.
- Treat findings as evidence, not decisions; ATC-243 takes precedence.
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
