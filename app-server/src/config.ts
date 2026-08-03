import { Config, ConfigProvider, Context, Effect, FileSystem, Layer, Option, Schema } from "effect"
import type { LogLevel } from "effect"
import * as SchemaIssue from "effect/SchemaIssue"
import * as os from "node:os"
import * as path from "node:path"
import { DEFAULT_PORT } from "./api.ts"

// The settled configuration pipeline (ATC-121). One precedence rule:
// command flags > environment (flat ATC_<KEY>) > config file (TOML) > defaults.
// Flags apply at the command seam in cli.ts; everything below the flag level
// resolves here, so the server and API-backed CLI commands read identical
// settings. Paths follow one XDG rule on every platform (macOS included),
// honoring XDG_* overrides:
//
//   config  $XDG_CONFIG_HOME/atc/config.toml  (~/.config/atc/config.toml)
//   data    $XDG_DATA_HOME/atc                (~/.local/share/atc)  -> atc.db
//   state   $XDG_STATE_HOME/atc               (~/.local/state/atc)  -> atc.log,
//                                             terminals/ (zmx sockets)
//
// ATC_CONFIG overrides the config file path; ATC_DATA_DIR the data directory.
// The TOML format never leaks past this module.

/** Environment shape consumed by the pipeline; tests inject their own. */
export type Env = Record<string, string | undefined>

/** Invalid or malformed configuration. `source` names the offending origin. */
export class ConfigLoadError extends Schema.TaggedErrorClass<ConfigLoadError>()("ConfigLoadError", {
  source: Schema.String,
  message: Schema.String,
}) {}

/**
 * The settled application configuration and data locations. Persistence,
 * logging, the server, and the CLI consume this service instead of re-deriving
 * locations or reading the environment themselves.
 */
export class AppConfig extends Context.Service<
  AppConfig,
  {
    /** TCP port the server listens on / the CLI connects to. */
    readonly port: number
    /** Minimum log level. */
    readonly logLevel: LogLevel.LogLevel
    /** Config file path that was consulted (it may not exist). */
    readonly configFile: string
    /** Directory holding durable data (the SQLite database). */
    readonly dataDir: string
    /** Directory holding server state (the log file). */
    readonly stateDir: string
    /** SQLite database file path. */
    readonly dbFile: string
    /** Structured JSON log file path. */
    readonly logFile: string
    /** zmx executable: an absolute path, or a name resolved on PATH. */
    readonly zmxExecutable: string
    /**
     * ATC-private zmx socket directory (ZMX_DIR for every zmx child). Only
     * ATC-owned sessions live here, so the inventory is authoritative and
     * orphan sockets are provably ours. Debug with `ZMX_DIR=<dir> zmx list`.
     */
    readonly terminalSocketDir: string
  }
>()("app-server/AppConfig") {}

// Empty environment values mean "unset" everywhere in the pipeline, matching
// how ConfigProvider.fromEnv treats them for the ATC_* settings.
const nonEmpty = (value: string | undefined) =>
  value !== undefined && value !== "" ? value : undefined

const xdgDir = (env: Env, xdgVar: string, homeFallback: ReadonlyArray<string>) => {
  const base = nonEmpty(env[xdgVar])
  return base !== undefined
    ? path.join(base, "atc")
    : path.join(nonEmpty(env["HOME"]) ?? os.homedir(), ...homeFallback, "atc")
}

/** The config file path: ATC_CONFIG, or the XDG config location. */
const configFilePath = (env: Env) =>
  nonEmpty(env["ATC_CONFIG"]) ??
  path.join(xdgDir(env, "XDG_CONFIG_HOME", [".config"]), "config.toml")

// The settings a config.toml may define. Keys are camelCase, matching the
// system-wide JSON convention; unknown keys fail fast (a typo silently
// falling back to a default would be a partial boot).
const FILE_KEYS = ["port", "logLevel", "dataDir", "zmxExecutable"] as const

const parseToml = (source: string, text: string) =>
  Effect.try({
    try: () => Bun.TOML.parse(text) as Record<string, unknown>,
    catch: (error) =>
      new ConfigLoadError({ source, message: error instanceof Error ? error.message : `${error}` }),
  }).pipe(
    Effect.filterOrFail(
      (table): table is Record<string, unknown> => typeof table === "object" && table !== null,
      () => new ConfigLoadError({ source, message: "config file must be a TOML table" }),
    ),
    Effect.tap((table) => {
      const unknown = Object.keys(table).filter(
        (key) => !(FILE_KEYS as ReadonlyArray<string>).includes(key),
      )
      return unknown.length === 0
        ? Effect.void
        : Effect.fail(
            new ConfigLoadError({
              source,
              message: `unknown key "${unknown[0]}" (known keys: ${FILE_KEYS.join(", ")})`,
            }),
          )
    }),
  )

/** Case-insensitive log level ("debug", "Info", "WARN", ...). */
const LOG_LEVELS: ReadonlyArray<LogLevel.LogLevel> = [
  "All",
  "Fatal",
  "Error",
  "Warn",
  "Info",
  "Debug",
  "Trace",
  "None",
]

const logLevelConfig = Config.string("logLevel").pipe(
  Config.mapOrFail((raw) => {
    const level = LOG_LEVELS.find((known) => known.toLowerCase() === raw.toLowerCase())
    return level !== undefined
      ? Effect.succeed(level)
      : Effect.fail(
          new Config.ConfigError(
            new Schema.SchemaError(
              new SchemaIssue.InvalidValue(Option.some(raw), {
                message: `logLevel must be one of ${LOG_LEVELS.map((l) => l.toLowerCase()).join(", ")}, got "${raw}"`,
              }),
            ),
          ),
        )
  }),
  Config.withDefault<LogLevel.LogLevel>("Info"),
)

/**
 * Load the settled configuration from `env` (environment precedence) and the
 * config file (file precedence), applying defaults last. Fails with
 * `ConfigLoadError` naming the offending source — never a partial result.
 */
export const load = (
  env: Env,
): Effect.Effect<AppConfig["Service"], ConfigLoadError, FileSystem.FileSystem> =>
  Effect.gen(function* () {
    // Every settled location must be absolute: a relative path would make the
    // config file, database, or log file depend on the working directory —
    // compiled behavior never may.
    const requireAbsolute = (value: string, what: string, source: string) =>
      value.startsWith("/")
        ? Effect.succeed(value)
        : Effect.fail(
            new ConfigLoadError({ source, message: `${what} must be absolute, got "${value}"` }),
          )

    const configFile = yield* requireAbsolute(
      configFilePath(env),
      "config file path",
      "ATC_CONFIG / XDG_CONFIG_HOME",
    )
    const fs = yield* FileSystem.FileSystem

    const fileTable = yield* fs.readFileString(configFile).pipe(
      Effect.flatMap((text) => parseToml(configFile, text)),
      Effect.catchTag("PlatformError", (error) =>
        error.reason._tag === "NotFound"
          ? Effect.succeed({} as Record<string, unknown>)
          : Effect.fail(new ConfigLoadError({ source: configFile, message: error.message })),
      ),
    )

    // Environment first, config file second; defaults per key. camelCase keys
    // map to CONSTANT_CASE env vars under the flat ATC_ prefix (port ->
    // ATC_PORT, logLevel -> ATC_LOG_LEVEL).
    const provider = ConfigProvider.orElse(
      ConfigProvider.fromEnv({ env: env as Record<string, string> }).pipe(
        ConfigProvider.nested("ATC"),
        ConfigProvider.constantCase,
      ),
      ConfigProvider.fromUnknown(fileTable),
    )

    const settings = yield* Config.all({
      port: Config.port("port").pipe(Config.withDefault(DEFAULT_PORT)),
      logLevel: logLevelConfig,
      dataDir: Config.string("dataDir").pipe(
        Config.withDefault(xdgDir(env, "XDG_DATA_HOME", [".local", "share"])),
      ),
      zmxExecutable: Config.string("zmxExecutable").pipe(Config.withDefault("zmx")),
    }).pipe(
      Effect.provideService(ConfigProvider.ConfigProvider, provider),
      Effect.catchTag("ConfigError", (error) =>
        Effect.fail(
          new ConfigLoadError({
            source: `${configFile} / ATC_* environment`,
            message: error.message.replace(/\s*\n\s*/g, " "),
          }),
        ),
      ),
    )

    // A relative executable path would resolve against the working
    // directory — compiled behavior never may. Bare names resolve on PATH.
    if (settings.zmxExecutable.includes("/") && !settings.zmxExecutable.startsWith("/")) {
      return yield* Effect.fail(
        new ConfigLoadError({
          source: `${configFile} / ATC_ZMX_EXECUTABLE`,
          message: `zmxExecutable must be a bare name or an absolute path, got "${settings.zmxExecutable}"`,
        }),
      )
    }

    const dataDir = yield* requireAbsolute(
      settings.dataDir,
      "dataDir",
      `${configFile} / ATC_DATA_DIR`,
    )
    const stateDir = yield* requireAbsolute(
      xdgDir(env, "XDG_STATE_HOME", [".local", "state"]),
      "state directory",
      "XDG_STATE_HOME",
    )
    return {
      port: settings.port,
      logLevel: settings.logLevel,
      configFile,
      dataDir,
      stateDir,
      dbFile: path.join(dataDir, "atc.db"),
      logFile: path.join(stateDir, "atc.log"),
      zmxExecutable: settings.zmxExecutable,
      terminalSocketDir: path.join(stateDir, "terminals"),
    }
  })

/** Production layer: loads from the real process environment. */
export const layer = Layer.effect(AppConfig)(load(process.env))
