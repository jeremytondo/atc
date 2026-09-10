package version

import (
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	// The shape `go install github.com/jeremytondo/atc/cmd/atc@v0.1.0`
	// embeds: a release-shaped module version with no workflow stamp.
	goInstall := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}
	vcs := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "3cf5189"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	dirty := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "3cf5189"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	for name, tt := range map[string]struct {
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		"stamp wins":             {"v0.2.0", vcs, true, "v0.2.0"},
		"go install stays devel": {"", goInstall, true, "devel"},
		"vcs revision":           {"", vcs, true, "devel-3cf5189"},
		"dirty tree marked":      {"", dirty, true, "devel-3cf5189-dirty"},
		"no vcs metadata":        {"", &debug.BuildInfo{}, true, "devel"},
		"no build info":          {"", nil, false, "unknown"},
	} {
		if got := resolve(tt.stamped, tt.info, tt.ok); got != tt.want {
			t.Errorf("%s: resolve(%q, ...) = %q, want %q", name, tt.stamped, got, tt.want)
		}
	}
}

func TestStringIsNeverEmpty(t *testing.T) {
	if String() == "" {
		t.Error("String() = \"\", want a build identity")
	}
}

func TestChannel(t *testing.T) {
	for v, want := range map[string]string{
		"v0.1.0":               ChannelStable,
		"v10.20.30":            ChannelStable,
		"v0.1.1-dev.3cf5189":   ChannelDev,
		"v0.1.1-dev.":          "",
		"v0.1.1-":              "",
		"v0.1.1-rc.1":          "",
		"v0.1":                 "",
		"0.1.0":                "",
		"devel":                "",
		"devel-3cf5189-dirty":  "",
		"unknown":              "",
		"":                     "",
		"vX.Y.Z":               "",
		"v0.1.0-dev":           "",
		"v0.1.1-dev.3cf5189-x": ChannelDev,
	} {
		if got := Channel(v); got != want {
			t.Errorf("Channel(%q) = %q, want %q", v, got, want)
		}
	}
}
