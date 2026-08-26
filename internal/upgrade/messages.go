package upgrade

// User-facing message rendering: pure and tested. The output always states
// what actually happened — including the deliberate semver-backwards move
// from a dev build to production.

import "fmt"

func upToDateMessage(version string) string {
	return fmt.Sprintf("atc %s is already up to date", version)
}

func replacedMessage(oldVersion, newVersion, target string) string {
	if oldVersion == newVersion {
		return fmt.Sprintf("reinstalled %s at %s", newVersion, target)
	}
	return fmt.Sprintf("replaced %s with %s at %s", oldVersion, newVersion, target)
}

// staleServerLine is the one clear line a headless (or declined) run gets;
// the state stays loud afterwards in `atc server status` and skew warnings.
func staleServerLine(serverVersion string) string {
	return fmt.Sprintf("server still on %s — run `atc server restart` or pass --restart", versionOrUnknown(serverVersion))
}

// restartPrompt carries the ATC-246 risk summary: terminals persist (zmx
// owns them durably); in-flight agent turns served over the API are
// interrupted. Default yes.
func restartPrompt(serverVersion string) string {
	return fmt.Sprintf("server is still on %s; restart it now? terminals persist, but any in-flight agent turns are interrupted [Y/n] ", versionOrUnknown(serverVersion))
}

func versionOrUnknown(version string) string {
	if version == "" {
		return "an unknown version"
	}
	return version
}
