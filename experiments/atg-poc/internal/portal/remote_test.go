package portal

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRemotePortalCommandQuotesEveryArgument(t *testing.T) {
	app := &App{
		remotePortal: ".local/bin/portal",
		remoteZMXBin: ".local/bin/zmx",
		remoteZMXDir: "/tmp/portal dir",
	}
	backend := newRemoteBackend(app)

	got := backend.remotePortalCommand("attach", "agent's shell")
	want := "'.local/bin/portal' '--zmx-dir' '/tmp/portal dir' '--zmx-bin' " +
		"'.local/bin/zmx' 'attach' 'agent'\"'\"'s shell'"
	if got != want {
		t.Fatalf("remotePortalCommand() = %q, want %q", got, want)
	}
}

func TestSSHArgsForceTTYOnlyForAttach(t *testing.T) {
	app := &App{remoteHost: "workstation"}
	backend := newRemoteBackend(app)

	attach := backend.sshArgs(true, "attach-command")
	list := backend.sshArgs(false, "list-command")
	if attach[0] != "-tt" {
		t.Fatalf("attach args do not force a TTY: %#v", attach)
	}
	if list[0] == "-tt" {
		t.Fatalf("list args unexpectedly force a TTY: %#v", list)
	}
	if attach[len(attach)-2] != "workstation" {
		t.Fatalf("SSH host missing from attach args: %#v", attach)
	}
}

func TestRemoteListSessionsUsesProtocolInventory(t *testing.T) {
	tempDir := t.TempDir()
	fakeSSH := filepath.Join(tempDir, "ssh")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"Protocol\":1,\"Sessions\":[\"agent.one\",\"terminal.two\"]}'\n"
	if err := os.WriteFile(fakeSSH, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	app := &App{
		in:           os.Stdin,
		out:          os.Stdout,
		errOut:       io.Discard,
		sshBin:       fakeSSH,
		remoteHost:   "workstation",
		remotePortal: ".local/bin/portal",
		remoteZMXBin: ".local/bin/zmx",
	}
	got, err := newRemoteBackend(app).ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agent.one", "terminal.two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListSessions() = %#v, want %#v", got, want)
	}
}

func TestRemoteListRejectsProtocolMismatch(t *testing.T) {
	tempDir := t.TempDir()
	fakeSSH := filepath.Join(tempDir, "ssh")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"Protocol\":99,\"Sessions\":[]}'\n"
	if err := os.WriteFile(fakeSSH, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	app := &App{
		in:           os.Stdin,
		out:          os.Stdout,
		errOut:       io.Discard,
		sshBin:       fakeSSH,
		remoteHost:   "workstation",
		remotePortal: "portal",
		remoteZMXBin: "zmx",
	}
	_, err := newRemoteBackend(app).ListSessions()
	if err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
		t.Fatalf("ListSessions() error = %v, want protocol mismatch", err)
	}
}

func TestRemoteAttachRetriesConnectionFailure(t *testing.T) {
	tempDir := t.TempDir()
	fakeSSH := filepath.Join(tempDir, "ssh")
	attemptsFile := filepath.Join(tempDir, "attempts")
	script := fmt.Sprintf(`#!/bin/sh
attempts=0
if [ -f %q ]; then
	attempts=$(cat %q)
fi
attempts=$((attempts + 1))
printf '%%s' "$attempts" > %q
if [ "$attempts" -eq 1 ]; then
	exit 255
fi
`, attemptsFile, attemptsFile, attemptsFile)
	if err := os.WriteFile(fakeSSH, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	app := &App{
		in:           os.Stdin,
		out:          os.Stdout,
		errOut:       io.Discard,
		sshBin:       fakeSSH,
		remoteHost:   "workstation",
		remotePortal: "portal",
		remoteZMXBin: "zmx",
	}
	if err := newRemoteBackend(app).Attach("agent.one"); err != nil {
		t.Fatal(err)
	}

	attempts, err := os.ReadFile(attemptsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(attempts)); got != "2" {
		t.Fatalf("SSH attempts = %s, want 2", got)
	}
}
