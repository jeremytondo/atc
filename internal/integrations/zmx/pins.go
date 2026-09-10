package zmx

// The releases this build knows (ATC-324). ATC is the only authority on
// which zmx it runs: each supported version names its exact release asset
// per target and the SHA-256 of both the archive and the executable
// inside it, measured from the real assets when the pin was set. Nothing
// discovers "latest", and a downloaded checksum file is never the
// authority.
//
// Desired is the version this build activates in an empty namespace. A
// namespace already running an older listed version keeps it until its
// sessions end and the server restarts; a version missing from the table
// is unsupported — its executable stays untouched, but no zmx operation
// runs through a client this build has not validated. Changing the pin
// means validating the new assets on every target, adding the new entry,
// and keeping the previous one so it can be reinstalled by identity.

import (
	"fmt"
	"runtime"
)

// Desired is the zmx version this build wants active.
const Desired = "0.6.0"

// Release is one zmx release: its assets by target.
type Release struct {
	// Assets is keyed by GOOS/GOARCH.
	Assets map[string]Asset
}

// Asset is one target's release archive and the executable it holds.
type Asset struct {
	Name             string
	ArchiveSHA256    string
	ExecutableSHA256 string
}

// releases is the shipped table. v0.6.0's sums were measured on
// 2026-09-10 from the GitHub release assets (each archive matched its
// published .sha256; each holds one regular file, zmx, mode 0755).
var releases = map[string]Release{
	"0.6.0": {Assets: map[string]Asset{
		"linux/amd64": {
			Name:             "zmx-0.6.0-linux-x86_64.tar.gz",
			ArchiveSHA256:    "309d913b982ae16eac2a854f411de40eccc0b64afed892aa02a0be351f0271c1",
			ExecutableSHA256: "5834eb204778d8c6f9a021f494a2771277ab557d03bbb91460c4cee89e4ae057",
		},
		"linux/arm64": {
			Name:             "zmx-0.6.0-linux-aarch64.tar.gz",
			ArchiveSHA256:    "c23f4b4ca80e144e329d042b91aae4859d23217ab07076b383af4134d97faac5",
			ExecutableSHA256: "24c4f67fc5bf9fee57c03bc5d3ceb0c494c4adeb69fbdbad621704a468eddac6",
		},
		"darwin/arm64": {
			Name:             "zmx-0.6.0-macos-aarch64.tar.gz",
			ArchiveSHA256:    "3f070c6e38cb3a48ddc131dbe956fd4c4ebf4ca6cfcc57c3acbb40994f169787",
			ExecutableSHA256: "326893ddfff2b835b85e32d1a798fbbcdea8fb6453db2b1b080c7653dd93f9ba",
		},
	}},
}

// releaseBase is where the assets are published, tokenless:
// releaseBase/v<version>/<asset>.
const releaseBase = "https://github.com/neurosnap/zmx/releases/download/"

// target is this machine's key into a release's assets.
func target() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// asset finds this machine's asset for a version in the table, naming the
// failure precisely: an unsupported version, or a supported one with no
// build for this platform.
func asset(table map[string]Release, version string) (Asset, error) {
	release, ok := table[version]
	if !ok {
		return Asset{}, fmt.Errorf("zmx %s is not a version this ATC build supports (supported: %s)", version, supportedVersions(table))
	}
	a, ok := release.Assets[target()]
	if !ok {
		return Asset{}, fmt.Errorf("zmx %s has no release build for %s", version, target())
	}
	return a, nil
}

func supportedVersions(table map[string]Release) string {
	var out string
	for version := range table {
		if out != "" {
			out += ", "
		}
		out += version
	}
	return out
}
