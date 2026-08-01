import { assert, describe, it } from "@effect/vitest"
import { cliCommandPaths, contractOperations, validateParity } from "../src/parity.ts"

// The real registry must be in full parity with the contract and the CLI;
// the synthetic cases prove the validator catches every violation CI is
// meant to block. Mapping one operation twice needs no test — the Record
// registry makes it structurally impossible.

describe("API-to-CLI parity", () => {
  it("the registry is in full parity with the contract and the CLI", () => {
    assert.deepStrictEqual(validateParity(), [])
  })

  it("reads the pinned operation ids from the generated document", () => {
    assert.sameMembers([...contractOperations()], ["getHealth", "getVersion"])
  })

  it("walks the real CLI tree, including process commands outside parity", () => {
    assert.includeMembers([...cliCommandPaths()], ["health", "version", "serve", "smoke"])
  })

  it("flags a public operation with no command mapping", () => {
    const errors = validateParity({ operations: [...contractOperations(), "listProjects"] })
    assert.deepStrictEqual(errors, ["public operation listProjects has no CLI command mapping"])
  })

  it("flags a mapping that references a missing operation", () => {
    const errors = validateParity({
      registry: { getStatus: "status" },
      operations: [],
      commands: ["status"],
    })
    assert.deepStrictEqual(errors, [
      "mapping for getStatus references an operation missing from the contract",
    ])
  })

  it("flags a mapping whose command is not a CLI command", () => {
    const errors = validateParity({
      registry: { getHealth: "healthz" },
      operations: ["getHealth"],
      commands: ["health"],
    })
    assert.deepStrictEqual(errors, [
      'mapping for getHealth claims "atc healthz", which is not a CLI command',
    ])
  })

  it("flags two operations claiming the same command path", () => {
    const errors = validateParity({
      registry: { getHealth: "health", getVersion: "health" },
      operations: ["getHealth", "getVersion"],
      commands: ["health"],
    })
    assert.deepStrictEqual(errors, [
      'command path "atc health" is claimed by both getHealth and getVersion',
    ])
  })

  it("flags an operation that is both mapped and excluded", () => {
    const errors = validateParity({
      registry: { getHealth: "health" },
      exclusions: { getHealth: "covered by a process command" },
      operations: ["getHealth"],
      commands: ["health"],
    })
    assert.deepStrictEqual(errors, ["operation getHealth is both mapped and excluded"])
  })

  it("flags an exclusion that references a missing operation", () => {
    const errors = validateParity({
      exclusions: { streamEvents: "streaming has no CLI shape yet" },
    })
    assert.deepStrictEqual(errors, [
      "exclusion for streamEvents references an operation missing from the contract",
    ])
  })
})
