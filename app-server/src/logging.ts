import { Effect, FileSystem, Layer, Logger, References } from "effect"
import { BuildInfo } from "./buildInfo.ts"
import { AppConfig } from "./config.ts"

// The structured logging baseline (ATC-121): every server log line goes to a
// JSON-lines file in the settled state directory; dev runs (running from
// source, BuildInfo.commit === "dev") additionally get a pretty stderr
// logger. The minimum level comes from the settled configuration
// (ATC_LOG_LEVEL / config.toml logLevel). Rotation and redaction are
// deliberately out of scope until there are secrets and consumers.

/**
 * Server logging: JSON file + dev stderr + span correlation, with the
 * configured minimum level. Provided to the serve stack only — CLI client
 * commands keep the default logger for unexpected defects and print their own
 * one-line diagnostics.
 */
export const layer: Layer.Layer<never, unknown, AppConfig | BuildInfo | FileSystem.FileSystem> =
  Layer.unwrap(
    Effect.gen(function* () {
      const config = yield* AppConfig
      const build = yield* BuildInfo
      const fs = yield* FileSystem.FileSystem
      yield* fs.makeDirectory(config.stateDir, { recursive: true })

      const loggers = [
        Logger.toFile(Logger.formatJson, config.logFile, { flag: "a" }),
        // Keeps log lines attached to the active request span as span events.
        Logger.tracerLogger,
        ...(build.commit === "dev" ? [Logger.consolePretty({ stderr: true })] : []),
      ]
      return Layer.mergeAll(
        Logger.layer(loggers, { mergeWithExisting: false }),
        Layer.succeed(References.MinimumLogLevel, config.logLevel),
      )
    }),
  )
