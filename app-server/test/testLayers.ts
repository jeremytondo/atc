import { Layer } from "effect"
import * as Directories from "../src/directories.ts"
import * as Persistence from "../src/persistence.ts"
import * as ProjectRepository from "../src/projectRepository.ts"

// Handler dependencies for in-process and ephemeral-listener tests: a real
// repository over a fresh in-memory database, and real directory checks.
export const TestRepositoryLayers = Layer.mergeAll(
  ProjectRepository.layer.pipe(Layer.provide(Persistence.layerFile(":memory:"))),
  Directories.layer,
)
