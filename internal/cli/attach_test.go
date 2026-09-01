package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

type fakeSessionAttacher struct {
	executable string
	argv       []string
	env        []string
	err        error
}

func (a fakeSessionAttacher) Preflight() error { return nil }
func (a fakeSessionAttacher) AttachCommand(string) (string, []string, []string, error) {
	return a.executable, a.argv, a.env, a.err
}

func TestPrepareAttachBuildsReusableChildCommand(t *testing.T) {
	argv := []string{"zmx", "attach", "term-abcde"}
	env := []string{"TERM=xterm-256color", "ZMX_DIR=/private"}
	cmd, err := PrepareAttach(api.Terminal{ID: "term-abcde", Status: api.TerminalRunning}, fakeSessionAttacher{
		executable: "/usr/bin/zmx", argv: argv, env: env,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/usr/bin/zmx" {
		t.Errorf("Path = %q", cmd.Path)
	}
	if diff := cmp.Diff(argv, cmd.Args); diff != "" {
		t.Errorf("Args (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(env, cmd.Env); diff != "" {
		t.Errorf("Env (-want +got):\n%s", diff)
	}
	argv[0], env[0] = "mutated", "mutated"
	if cmd.Args[0] != "zmx" || cmd.Env[0] != "TERM=xterm-256color" {
		t.Fatal("prepared command aliases adapter-owned slices")
	}
}

func TestPrepareAttachRefusesUnsafeHandover(t *testing.T) {
	for name, tc := range map[string]struct {
		terminal api.Terminal
		attacher fakeSessionAttacher
		want     string
	}{
		"not running": {
			terminal: api.Terminal{ID: "term-abcde", Status: api.TerminalMissing},
			want:     "not running",
		},
		"adapter error": {
			terminal: api.Terminal{ID: "term-abcde", Status: api.TerminalRunning},
			attacher: fakeSessionAttacher{err: errors.New("socket missing")},
			want:     "socket missing",
		},
		"empty command": {
			terminal: api.Terminal{ID: "term-abcde", Status: api.TerminalRunning},
			attacher: fakeSessionAttacher{executable: "/usr/bin/zmx"},
			want:     "empty command",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PrepareAttach(tc.terminal, tc.attacher)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PrepareAttach = %v; want %q", err, tc.want)
			}
		})
	}
}
