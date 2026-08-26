// Package version resolves the binary's build identity (ATC-261).
//
// Version is stamped by the release workflow via `-ldflags -X` (see
// .goreleaser.yaml); only workflow-built binaries ever claim a
// vX.Y.Z-shaped version. That property is what makes client/server skew
// detection and `atc upgrade`'s staleness comparison trustworthy: local
// builds deliberately stay on the buildinfo fallback (devel-<sha>) instead
// of `git describe`-style release-shaped names.
package version

import "runtime/debug"

// Version is the release identity stamped at build time. Empty in every
// non-workflow build.
var Version string

// String returns the build identity used everywhere a version is shown or
// compared. Released builds carry the stamped version; source builds fall
// back to the VCS revision. One limit: builds without VCS metadata all
// report "devel" and cannot be told apart.
func String() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	version := info.Main.Version
	if version != "" && version != "(devel)" {
		return version
	}
	revision, dirty := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" {
		return "devel"
	}
	if dirty {
		return "devel-" + revision + "-dirty"
	}
	return "devel-" + revision
}
