import { Layer } from "effect"
import { BuildInfo } from "../src/buildInfo.ts"

export const testBuildInfo = {
  version: "1.2.3-test",
  commit: "abc1234",
  builtAt: "2026-07-31T00:00:00Z",
} as const

export const TestBuildInfoLayer = Layer.succeed(BuildInfo)(testBuildInfo)
