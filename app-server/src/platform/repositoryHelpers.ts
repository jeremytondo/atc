import { Effect, Option } from "effect"

// Shared result-shaping for the repositories: the row→record plumbing that
// was copied verbatim across all three. SQL stays inside each repository —
// these helpers never see a query.

/** Row→record result helpers for one repository. */
export const rowHelpers = <Row, Rec>(entity: string, toRecord: (row: Row) => Rec) => ({
  /** The first row as a mapped Option (id lookups return at most one). */
  firstRecord: (rows: ReadonlyArray<Row>): Option.Option<Rec> =>
    Option.fromNullishOr(rows[0]).pipe(Option.map(toRecord)),
  /** For updates whose caller holds the record: a vanished row is a bug. */
  requireFirst:
    (what: string) =>
    (rows: ReadonlyArray<Row>): Effect.Effect<Rec> =>
      rows[0] !== undefined
        ? Effect.succeed(toRecord(rows[0]))
        : Effect.die(new Error(`${what}: ${entity} row vanished mid-update`)),
})

/** `require` from `get`: unwrap the Option or fail with the domain error. */
export const requireFound =
  <A, E>(notFound: () => E) =>
  (found: Effect.Effect<Option.Option<A>>): Effect.Effect<A, E> =>
    found.pipe(
      Effect.flatMap(
        Option.match({ onNone: () => Effect.fail(notFound()), onSome: Effect.succeed }),
      ),
    )
