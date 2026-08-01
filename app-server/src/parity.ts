import { atc } from "./cli.ts"
import { openApiDocument } from "./openapi.ts"

// The API-to-CLI parity registry: every public contract operation maps to
// exactly one CLI command (or carries an explicit, justified exclusion).
// parity.test.ts runs validateParity in CI, so the registry cannot drift from
// the contract or the CLI. Process-level commands (serve, smoke) are CLI-only
// and deliberately outside API parity.

/**
 * Canonical CLI command path (space-separated, under `atc`) for every public
 * operation id. A Record keys each operation once, so mapping an operation
 * twice is structurally impossible.
 */
export const registry: Readonly<Record<string, string>> = {
  getHealth: "health",
  getVersion: "version",
}

/** Operations deliberately not exposed as CLI commands, keyed to a justification. */
export const exclusions: Readonly<Record<string, string>> = {}

/**
 * Operation ids of the public API, read from the in-process generated
 * document — the same `OpenApi.fromApi` derivation the checked-in artifact
 * and generated clients use, so ids cannot drift from the source of truth.
 */
export const contractOperations = (): ReadonlyArray<string> =>
  Object.values(openApiDocument.paths).flatMap((operations) =>
    Object.values(operations).map((operation) => operation.operationId),
  )

/** The command-tree shape parity needs; structurally satisfied by Effect CLI commands. */
interface CliCommand {
  readonly name: string
  readonly subcommands: ReadonlyArray<{ readonly commands: ReadonlyArray<CliCommand> }>
}

/** Every command path registered under `atc`, walked from the real CLI tree. */
export const cliCommandPaths = (): ReadonlyArray<string> => {
  const paths: Array<string> = []
  const walk = (command: CliCommand, prefix: string) => {
    const path = prefix === "" ? command.name : `${prefix} ${command.name}`
    paths.push(path)
    for (const group of command.subcommands) {
      for (const subcommand of group.commands) {
        walk(subcommand, path)
      }
    }
  }
  for (const group of atc.subcommands) {
    for (const subcommand of group.commands) {
      walk(subcommand, "")
    }
  }
  return paths
}

/**
 * Validate a registry against the contract's operations and the CLI's
 * commands. Defaults to the real ones; overrides exist so tests can prove
 * each failure mode. Returns human-readable violations; an empty array means
 * full parity.
 */
export const validateParity = ({
  commands = cliCommandPaths(),
  exclusions: excluded = exclusions,
  operations = contractOperations(),
  registry: entries = registry,
}: {
  readonly commands?: ReadonlyArray<string>
  readonly exclusions?: Readonly<Record<string, string>>
  readonly operations?: ReadonlyArray<string>
  readonly registry?: Readonly<Record<string, string>>
} = {}): ReadonlyArray<string> => {
  const errors: Array<string> = []

  const claimants = new Map<string, string>()
  for (const [operation, command] of Object.entries(entries)) {
    const claimant = claimants.get(command)
    if (claimant !== undefined) {
      errors.push(`command path "atc ${command}" is claimed by both ${claimant} and ${operation}`)
    }
    claimants.set(command, operation)
    if (!commands.includes(command)) {
      errors.push(`mapping for ${operation} claims "atc ${command}", which is not a CLI command`)
    }
    if (!operations.includes(operation)) {
      errors.push(`mapping for ${operation} references an operation missing from the contract`)
    }
    if (operation in excluded) {
      errors.push(`operation ${operation} is both mapped and excluded`)
    }
  }

  for (const operation of Object.keys(excluded)) {
    if (!operations.includes(operation)) {
      errors.push(`exclusion for ${operation} references an operation missing from the contract`)
    }
  }

  for (const operation of operations) {
    if (!(operation in entries) && !(operation in excluded)) {
      errors.push(`public operation ${operation} has no CLI command mapping`)
    }
  }

  return errors
}
