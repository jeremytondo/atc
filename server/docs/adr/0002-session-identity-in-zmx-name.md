# Session identity lives in the zmx name; no atc registry (MVP)

> **Terminology note (2026-07):** This ADR predates the atc rename. "atc" is now the atc server (`atc`); the `atc:` session-name prefix described below is historical.

> **Superseded by [ADR 0006](0006-atc-owned-session-identity.md).**
> The backend agent-sessions pass introduces a SQLite session registry and a
> stable atc-owned `Session.id`; multiplexer names are now private
> implementation details.
> The reasoning below is retained for history.

For the terminal-session MVP, atc keeps **no** session registry or database.
The `zmx` session name is the sole source of session identity and the join key
for any future state. Liveness and basic metadata (`created`, `start_dir`,
`cmd`) are read from `zmx list` on demand.

## Naming scheme

`atc:<kind>:<id>` where `kind ∈ {item, project, free}`:

- `atc:item:DEV-22` — item-scoped
- `atc:project:my-app` — project-scoped (slug)
- `atc:free:k7m2x9qp` — unscoped; id is a `crypto/rand` token

Colon is the separator (matching the workstation's existing `atc:claude-code`
convention) so parse-back is a plain `SplitN(name, ":", 3)`. This relies on item
ids and project slugs never containing a colon. "atc-managed" means a name
matches this 3-part pattern — a naive `atc:` prefix match is **not** used, so
hand-made sessions like `atc:claude-code` are correctly excluded.

## Why no registry now

- The MVP's only job is proving the spawn → inject → submit → persist → attach
  loop. It does not read any state a registry would hold.
- Adding a registry later is **additive, not a rewrite**: it is a table keyed by
  the session name that decorates sessions discovered from `zmx list`. The
  abstraction, naming, and API response shapes do not have to change — stateful
  fields are added, not reshaped.
- Avoids introducing atc's first database (and the corresponding spec
  amendment) before any feature needs it.

## Known future trigger

`zmx` already exposes `created`, `start_dir`, and `cmd`. The one datum it cannot
recover after the fact is the **originating prompt text**. When atc needs
that (or richer status/history), it adds the name-keyed registry. That is the
expected point at which this decision is revisited.

## Consequences

- `start` is strict create (fails if the deterministic name already exists);
  `send` requires the session to exist. There is deliberately no
  "start-or-attach" call, so a client cannot accidentally inject a prompt into
  the wrong live agent.
- Listing must filter to the 3-part `atc:*` pattern and tolerate unreachable
  / malformed entries (e.g. `status=unreachable`) without failing.
