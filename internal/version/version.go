// Package version resolves the binary's build identity (ATC-261).
//
// Version is stamped by the release workflow via `-ldflags -X` (see
// .goreleaser.yaml); only workflow-built binaries ever claim a
// vX.Y.Z-shaped version. That property is what makes release
// identification and `atc upgrade`'s staleness comparison trustworthy, so every
// unstamped build — including `go install ...@vX.Y.Z`, whose buildinfo
// carries the module version — resolves to a devel identity instead.
package version

import (
	"runtime/debug"
	"strings"
)

// Release channels a published build belongs to (ATC-325). Channel
// identity is read off the stamped version: a production cut is plain
// vX.Y.Z, a dev cut carries the "-dev." pre-release the release workflow
// stamps (.goreleaser.yaml snapshot template). Every other identity is
// unpublished — there is no artifact to download for it.
const (
	ChannelStable = "stable"
	ChannelDev    = "dev"
)

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

// Channel classifies a build identity: ChannelStable, ChannelDev, or ""
// for an unpublished build (devel, devel-<sha>, unknown, or anything not
// stamped by the release workflow).
func Channel(v string) string {
	if !strings.HasPrefix(v, "v") {
		return ""
	}
	base, pre, hyphen := strings.Cut(v[1:], "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return ""
	}
	for _, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return ""
		}
	}
	switch {
	case !hyphen:
		return ChannelStable
	case strings.HasPrefix(pre, "dev.") && len(pre) > len("dev."):
		return ChannelDev
	}
	return ""
}
