# Session identity lives in the zmx name; no Cockpit registry (MVP)

> **Superseded by [ADR 0006](0006-cockpit-owned-session-identity.md).**
> The backend agent-sessions pass introduces a SQLite session registry and a
> stable Cockpit-owned `Session.id`; multiplexer names are now private
> implementation details.
> The reasoning below is retained for history.

For the terminal-session MVP, Cockpit keeps **no** session registry or database.
The `zmx` session name is the sole source of session identity and the join key
for any future state. Liveness and basic metadata (`created`, `start_dir`,
`cmd`) are read from `zmx list` on demand.

## Naming scheme

`cockpit:<kind>:<id>` where `kind ∈ {item, project, free}`:

- `cockpit:item:DEV-22` — item-scoped
- `cockpit:project:my-app` — project-scoped (slug)
- `cockpit:free:k7m2x9qp` — unscoped; id is a `crypto/rand` token

Colon is the separator (matching the workstation's existing `cockpit:claude-code`
convention) so parse-back is a plain `SplitN(name, ":", 3)`. This relies on item
ids and project slugs never containing a colon. "Cockpit-managed" means a name
matches this 3-part pattern — a naive `cockpit:` prefix match is **not** used, so
hand-made sessions like `cockpit:claude-code` are correctly excluded.

## Why no registry now

- The MVP's only job is proving the spawn → inject → submit → persist → attach
  loop. It does not read any state a registry would hold.
- Adding a registry later is **additive, not a rewrite**: it is a table keyed by
  the session name that decorates sessions discovered from `zmx list`. The
  abstraction, naming, and API response shapes do not have to change — stateful
  fields are added, not reshaped.
- Avoids introducing Cockpit's first database (and the corresponding spec
  amendment) before any feature needs it.

## Known future trigger

`zmx` already exposes `created`, `start_dir`, and `cmd`. The one datum it cannot
recover after the fact is the **originating prompt text**. When Cockpit needs
that (or richer status/history), it adds the name-keyed registry. That is the
expected point at which this decision is revisited.

## Consequences

- `start` is strict create (fails if the deterministic name already exists);
  `send` requires the session to exist. There is deliberately no
  "start-or-attach" call, so a client cannot accidentally inject a prompt into
  the wrong live agent.
- Listing must filter to the 3-part `cockpit:*` pattern and tolerate unreachable
  / malformed entries (e.g. `status=unreachable`) without failing.
