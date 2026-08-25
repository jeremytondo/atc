package paths

import (
	"path/filepath"
	"testing"
)

func TestXDGOverridesHonored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	for name, tc := range map[string]struct {
		got  func() (string, error)
		want string
	}{
		"config": {ConfigFile, "/custom/config/atc/config.toml"},
		"token":  {AuthTokenFile, "/custom/data/atc/auth-token"},
		"log":    {LogFile, "/custom/state/atc/atc.log"},
	} {
		path, err := tc.got()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if path != filepath.FromSlash(tc.want) {
			t.Errorf("%s = %q, want %q", name, path, tc.want)
		}
	}
}

func TestDefaultsUnderHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/example")
	path, err := AuthTokenFile()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.FromSlash("/home/example/.local/share/atc/auth-token"); path != want {
		t.Errorf("token path = %q, want %q", path, want)
	}
}
