import packageJson from "../package.json";

/** Build metadata carried by the running executable. */
export interface BuildInfo {
  /** Application version, e.g. "0.1.0". */
  version: string;
  /** Source revision the executable was built from. */
  commit: string;
  /** ISO-8601 timestamp of the build. */
  builtAt: string;
}

/**
 * Build metadata for running from source. Compiled executables will inject
 * real commit/builtAt values at build time (ATC-88).
 */
export function developmentBuildInfo(): BuildInfo {
  return {
    version: packageJson.version,
    commit: "development",
    builtAt: "development",
  };
}
