package version

import "testing"

func TestStringPrefersStampedVersion(t *testing.T) {
	defer func(previous string) { Version = previous }(Version)
	Version = "v0.1.0"
	if got := String(); got != "v0.1.0" {
		t.Errorf("String() = %q, want the stamped version", got)
	}
}

func TestStringFallbackIsNeverEmpty(t *testing.T) {
	defer func(previous string) { Version = previous }(Version)
	Version = ""
	if String() == "" {
		t.Error("String() = \"\", want a buildinfo-derived identity")
	}
}
