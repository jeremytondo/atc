// Package version resolves the binary's build identity (ATC-261).
//
// Version is stamped by the release workflow via `-ldflags -X` (see
// .goreleaser.yaml); only workflow-built binaries ever claim a
// vX.Y.Z-shaped version. That property is what makes client/server skew
// detection and `atc upgrade`'s staleness comparison trustworthy, so every
// unstamped build — including `go install ...@vX.Y.Z`, whose buildinfo
// carries the module version — resolves to a devel identity instead.
package version

import "runtime/debug"

// Version is the release identity stamped at build time. Empty in every
// non-workflow build.
var Version string

// String returns the build identity used everywhere a version is shown or
// compared.
func String() string {
	info, ok := debug.ReadBuildInfo()
	return resolve(Version, info, ok)
}

// resolve is the pure resolution order: the workflow stamp wins, then VCS
// metadata (devel-<sha>, devel-<sha>-dirty), then devel/unknown.
// info.Main.Version is deliberately ignored — `go install ...@vX.Y.Z`
// embeds it, and a non-workflow build claiming a release-shaped version
// would break the "only workflow builds" invariant and make `atc upgrade`
// short-circuit. One limit: builds without VCS metadata all report "devel"
// and cannot be told apart.
func resolve(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != "" {
		return stamped
	}
	if !ok {
		return "unknown"
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
