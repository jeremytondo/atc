import { Config, ConfigProvider, Context, Effect, FileSystem, Layer, Option, Schema } from "effect"
import type { LogLevel } from "effect"
import * as SchemaIssue from "effect/SchemaIssue"
import * as os from "node:os"
import * as path from "node:path"
import { DEFAULT_PORT } from "../api/contract.ts"

// The settled configuration pipeline (ATC-121). One precedence rule:
// command flags > environment (flat ATC_<KEY>) > config file (TOML) > defaults.
// Flags apply at the command seam in cli/; everything below the flag level
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

/**
 * The stable agent-context variable vocabulary (ATC-131). ATC sets these in
 * the environment of processes it launches (terminal sessions above all);
 * `atc context` reports whichever are present. ATC_WORKSPACE_ID and
 * ATC_THREAD_ID are reserved for resources that do not exist yet — the names
 * are fixed now so consumers never have to migrate.
 */
export const CONTEXT_VARIABLES = [
  "ATC_ENDPOINT",
  "ATC_PROJECT_ID",
  "ATC_WORKSPACE_ID",
  "ATC_THREAD_ID",
  "ATC_TERMINAL_ID",
] as const

export type ContextVariable = (typeof CONTEXT_VARIABLES)[number]

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
    /**
     * Address the server binds. Default 127.0.0.1 (loopback only). The
     * practical non-loopback value is 0.0.0.0: binding a single non-loopback
     * address drops the loopback listener and breaks local clients (zmx
     * children, the `atc start` health probe, the Web UI). Documented, not
     * policed — any address is accepted.
     */
    readonly bind: string
    /**
     * App Server base URL from ATC_ENDPOINT (set for processes ATC
     * launches); undefined means "derive from the local port". Environment
     * only — never a config-file key, because it describes the launching
     * process's context, not the user's configuration.
     */
    readonly endpoint: string | undefined
    /** The ATC_* agent-context variables present in this process. */
    readonly context: Readonly<Partial<Record<ContextVariable, string>>>
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
    /** Bearer token file path (remote-access credential; 0600 in dataDir). */
    readonly tokenFile: string
    /** Structured JSON log file path. */
    readonly logFile: string
    /** Background-service pidfile path (written by `atc start`). */
    readonly pidFile: string
    /** zmx executable: an absolute path, or a name resolved on PATH. */
    readonly zmxExecutable: string
    /** Codex CLI executable: an absolute path, or a name resolved on PATH. */
    readonly codexExecutable: string
    /** Claude Code executable: an absolute path, or a name resolved on PATH. */
    readonly claudeExecutable: string
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
const FILE_KEYS = [
  "port",
  "bind",
  "logLevel",
  "dataDir",
  "zmxExecutable",
  "codexExecutable",
  "claudeExecutable",
] as const

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
      bind: Config.string("bind").pipe(Config.withDefault("127.0.0.1")),
      logLevel: logLevelConfig,
      dataDir: Config.string("dataDir").pipe(
        Config.withDefault(xdgDir(env, "XDG_DATA_HOME", [".local", "share"])),
      ),
      zmxExecutable: Config.string("zmxExecutable").pipe(Config.withDefault("zmx")),
      codexExecutable: Config.string("codexExecutable").pipe(Config.withDefault("codex")),
      claudeExecutable: Config.string("claudeExecutable").pipe(Config.withDefault("claude")),
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
    const executables = [
      ["zmxExecutable", "ATC_ZMX_EXECUTABLE", settings.zmxExecutable],
      ["codexExecutable", "ATC_CODEX_EXECUTABLE", settings.codexExecutable],
      ["claudeExecutable", "ATC_CLAUDE_EXECUTABLE", settings.claudeExecutable],
    ] as const
    for (const [key, envVar, value] of executables) {
      if (value.includes("/") && !value.startsWith("/")) {
        return yield* Effect.fail(
          new ConfigLoadError({
            source: `${configFile} / ${envVar}`,
            message: `${key} must be a bare name or an absolute path, got "${value}"`,
          }),
        )
      }
    }

    // Agent context is captured verbatim (present means non-empty). Only
    // ATC_ENDPOINT is interpreted, so only it is validated: a malformed
    // value would otherwise surface as a confusing request failure.
    const context: Partial<Record<ContextVariable, string>> = {}
    for (const name of CONTEXT_VARIABLES) {
      const value = nonEmpty(env[name])
      if (value !== undefined) context[name] = value
    }
    const endpoint = context.ATC_ENDPOINT
    if (endpoint !== undefined) {
      const parsed = URL.canParse(endpoint) ? new URL(endpoint) : undefined
      if (parsed === undefined || (parsed.protocol !== "http:" && parsed.protocol !== "https:")) {
        return yield* Effect.fail(
          new ConfigLoadError({
            source: "ATC_ENDPOINT",
            message: `endpoint must be an http(s) URL, got "${endpoint}"`,
          }),
        )
      }
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
      bind: settings.bind,
      endpoint,
      context,
      logLevel: settings.logLevel,
      configFile,
      dataDir,
      stateDir,
      dbFile: path.join(dataDir, "atc.db"),
      tokenFile: path.join(dataDir, "auth-token"),
      logFile: path.join(stateDir, "atc.log"),
      pidFile: path.join(stateDir, "atc.pid"),
      zmxExecutable: settings.zmxExecutable,
      codexExecutable: settings.codexExecutable,
      claudeExecutable: settings.claudeExecutable,
      terminalSocketDir: path.join(stateDir, "terminals"),
    }
  })

/** Production layer: loads from the real process environment. */
export const layer = Layer.effect(AppConfig)(load(process.env))
