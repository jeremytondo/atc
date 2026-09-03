package application

import (
	"errors"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
)

// Mode validation is a pure gate on the three selectors: at most one, and
// placement never counts. The wire-level behavior of each mode is covered
// by the server tests.
func TestMode(t *testing.T) {
	cases := map[string]struct {
		params api.TerminalCreateParams
		want   launchMode
		err    error
	}{
		"shell":          {api.TerminalCreateParams{SpaceID: "spce-aaaaa", Directory: "/x", Name: "n"}, modeShell, nil},
		"command":        {api.TerminalCreateParams{Command: "hx"}, modeCommand, nil},
		"app":            {api.TerminalCreateParams{AppID: "claude/tui"}, modeApp, nil},
		"thread":         {api.TerminalCreateParams{ThreadID: "thrd-aaaaa"}, modeThread, nil},
		"command+app":    {api.TerminalCreateParams{Command: "hx", AppID: "claude/tui"}, modeShell, ErrLaunchModeConflict},
		"command+thread": {api.TerminalCreateParams{Command: "hx", ThreadID: "thrd-aaaaa"}, modeShell, ErrLaunchModeConflict},
		"app+thread":     {api.TerminalCreateParams{AppID: "claude/tui", ThreadID: "thrd-aaaaa"}, modeShell, ErrLaunchModeConflict},
		"all three":      {api.TerminalCreateParams{Command: "hx", AppID: "claude/tui", ThreadID: "thrd-aaaaa"}, modeShell, ErrLaunchModeConflict},
	}
	for name, tc := range cases {
		got, err := mode(tc.params)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("%s: mode = %v, %v; want %v, %v", name, got, err, tc.want, tc.err)
		}
	}
	stripped := placement(api.TerminalCreateParams{SpaceID: "spce-aaaaa", Directory: "/x", Name: "n", Command: "hx", AppID: "a/b", ThreadID: "t"})
	if stripped != (api.TerminalCreateParams{SpaceID: "spce-aaaaa", Directory: "/x", Name: "n"}) {
		t.Errorf("placement = %+v", stripped)
	}
}
