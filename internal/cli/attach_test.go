package cli

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

type fakeAttacher struct{ ids []string }

func (fakeAttacher) Preflight() error { return nil }
func (a *fakeAttacher) AttachCommand(id string) (string, []string, []string, error) {
	a.ids = append(a.ids, id)
	return "/usr/bin/zmx", []string{"zmx", "attach", id}, []string{"ZMX_SOCKET_DIR=/tmp/sock", "TERM=xterm"}, nil
}

// PrepareAttach is the one preparation both clients share: a running
// terminal becomes the driver's exact handover as a runnable command; any
// other status is refused before the driver is consulted.
func TestPrepareAttach(t *testing.T) {
	attacher := &fakeAttacher{}
	cmd, err := PrepareAttach(api.Terminal{ID: "term-abcde", Status: api.TerminalRunning}, attacher)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/usr/bin/zmx" {
		t.Errorf("Path = %q", cmd.Path)
	}
	if diff := cmp.Diff([]string{"zmx", "attach", "term-abcde"}, cmd.Args); diff != "" {
		t.Errorf("Args (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"ZMX_SOCKET_DIR=/tmp/sock", "TERM=xterm"}, cmd.Env); diff != "" {
		t.Errorf("Env (-want +got):\n%s", diff)
	}

	for _, status := range []api.TerminalStatus{api.TerminalExited, api.TerminalUnreachable, api.TerminalMissing} {
		if _, err := PrepareAttach(api.Terminal{ID: "term-abcde", Status: status}, attacher); err == nil {
			t.Errorf("%s terminal prepared an attach", status)
		}
	}
	if diff := cmp.Diff([]string{"term-abcde"}, attacher.ids); diff != "" {
		t.Errorf("driver consulted for non-running terminals (-want +got):\n%s", diff)
	}
}
