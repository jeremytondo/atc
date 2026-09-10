package zmx

// Runtime selection, status, activation, and startup tests (ATC-324).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
)

// installed writes a controlled executable under a runtime directory as
// the installer would, and returns its selection.
func installed(t *testing.T, dir, version string) (string, selection) {
	t.Helper()
	exe := fakeZmx(version)
	vdir := filepath.Join(dir, executableName, version)
	if err := os.MkdirAll(vdir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(vdir, executableName)
	if err := os.WriteFile(path, exe, 0o755); err != nil {
		t.Fatal(err)
	}
	sel, err := selectionFor(version, path)
	if err != nil {
		t.Fatal(err)
	}
	return path, sel
}

func newRuntimeWith(t *testing.T, dir, selectionFile string, sel *selection) *Runtime {
	t.Helper()
	if sel != nil {
		if err := writeSelection(selectionFile, *sel); err != nil {
			t.Fatal(err)
		}
	}
	table := map[string]Release{"0.6.0": {Assets: map[string]Asset{target(): {Name: "n", ArchiveSHA256: "a", ExecutableSHA256: sha256hex(fakeZmx("0.6.0"))}}}}
	return NewRuntime(RuntimeOptions{RuntimeDir: dir, SelectionFile: selectionFile, Releases: table, Desired: "0.6.0"})
}

func TestResolveActiveOffline(t *testing.T) {
	dir := t.TempDir()
	selFile := filepath.Join(t.TempDir(), "runtime.json")
	path, sel := installed(t, dir, "0.6.0")
	rt := newRuntimeWith(t, dir, selFile, &sel)

	got, err := rt.Executable()
	if err != nil || got != path {
		t.Fatalf("Executable() = %q, %v; want %q offline", got, err, path)
	}
	// The shared resolver the CLI uses agrees, with no Runtime at all.
	if via, err := ResolveActive(selFile); err != nil || via != path {
		t.Fatalf("ResolveActive = %q, %v; want %q", via, err, path)
	}
	if !rt.Available() {
		t.Error("Available() = false for a validated runtime")
	}
}

func TestNoSelectionIsUnavailableNotEmpty(t *testing.T) {
	rt := newRuntimeWith(t, t.TempDir(), filepath.Join(t.TempDir(), "runtime.json"), nil)
	if _, err := rt.Executable(); err == nil {
		t.Fatal("Executable() succeeded with no selection")
	}
	if _, err := ResolveActive(filepath.Join(t.TempDir(), "absent.json")); err == nil || !strings.Contains(err.Error(), "no terminal runtime is active") {
		t.Errorf("ResolveActive(absent) = %v", err)
	}
}

func TestCorruptSelectionFailsVisibly(t *testing.T) {
	selFile := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(selFile, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := newRuntimeWith(t, t.TempDir(), selFile, nil)
	_, err := rt.Executable()
	if err == nil || !strings.Contains(err.Error(), "will not guess") {
		t.Fatalf("Executable() = %v, want a visible refusal that guesses nothing", err)
	}
}

func TestUnsupportedActiveVersion(t *testing.T) {
	dir := t.TempDir()
	selFile := filepath.Join(t.TempDir(), "runtime.json")
	_, sel := installed(t, dir, "0.5.0") // installed and valid, but not in the table
	rt := newRuntimeWith(t, dir, selFile, &sel)
	_, err := rt.Executable()
	if err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("Executable() = %v, want an incompatibility refusal", err)
	}
	status := rt.Status()
	if status.Active != "0.5.0" || !status.Pending {
		t.Errorf("status = %+v, want active 0.5.0 pending", status)
	}
}

func TestDamagedExecutableIsRepairable(t *testing.T) {
	dir := t.TempDir()
	selFile := filepath.Join(t.TempDir(), "runtime.json")
	path, sel := installed(t, dir, "0.6.0")
	rt := newRuntimeWith(t, dir, selFile, &sel)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	rt.load()
	if _, err := rt.Executable(); err == nil {
		t.Fatal("Executable() succeeded with the file removed")
	}
	got, ok := rt.needsRepair()
	if !ok || got.Version != "0.6.0" {
		t.Fatalf("needsRepair = %+v, %v; want the recorded runtime", got, ok)
	}
}

func TestStatusReportsDesiredPending(t *testing.T) {
	dir := t.TempDir()
	selFile := filepath.Join(t.TempDir(), "runtime.json")
	_, sel := installed(t, dir, "0.6.0")
	rt := NewRuntime(RuntimeOptions{RuntimeDir: dir, SelectionFile: selFile,
		Releases: map[string]Release{
			"0.6.0": {Assets: map[string]Asset{target(): {Name: "n", ArchiveSHA256: "a", ExecutableSHA256: sha256hex(fakeZmx("0.6.0"))}}},
			"0.7.0": {Assets: map[string]Asset{target(): {Name: "n", ArchiveSHA256: "a", ExecutableSHA256: sha256hex(fakeZmx("0.7.0"))}}},
		}, Desired: "0.7.0"})
	if err := writeSelection(selFile, sel); err != nil {
		t.Fatal(err)
	}
	rt.load()
	rt.setBlocked("zmx 0.7.0 is installed; switching from 0.6.0 waits for all sessions to end and the server to restart")
	status := rt.Status()
	want := api.IntegrationRuntime{Active: "0.6.0", Desired: "0.7.0", Executable: sel.Executable, Pending: true,
		Detail: "zmx 0.7.0 is installed; switching from 0.6.0 waits for all sessions to end and the server to restart"}
	if status != want {
		t.Errorf("status = %+v\nwant %+v", status, want)
	}
	// The active runtime stays usable while the desired one is pending.
	if !rt.Available() {
		t.Error("Available() = false while an older supported runtime is active")
	}
}

// activation commits the selection only when the emptiness proof passes,
// and never before.
func TestActivateGatesOnEmptiness(t *testing.T) {
	dir := t.TempDir()
	selFile := filepath.Join(t.TempDir(), "runtime.json")
	path, sel := installed(t, dir, "0.6.0")
	rt := newRuntimeWith(t, dir, selFile, nil)

	notEmpty := func(context.Context) (string, error) { return "sessions remain", nil }
	if err := rt.activate(context.Background(), sel, notEmpty); err == nil || !strings.Contains(err.Error(), "sessions remain") {
		t.Fatalf("activate(not empty) = %v, want the reason", err)
	}
	if _, err := os.Stat(selFile); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused activation wrote a selection")
	}
	fails := func(context.Context) (string, error) { return "", errors.New("inventory down") }
	if err := rt.activate(context.Background(), sel, fails); err == nil || !strings.Contains(err.Error(), "inventory down") {
		t.Fatalf("activate(inventory error) = %v, want the error (never treated as empty)", err)
	}
	empty := func(context.Context) (string, error) { return "", nil }
	if err := rt.activate(context.Background(), sel, empty); err != nil {
		t.Fatalf("activate(empty) = %v", err)
	}
	if got, err := ResolveActive(selFile); err != nil || got != path {
		t.Fatalf("after activation ResolveActive = %q, %v; want %q", got, err, path)
	}
}

// A selection recording a version this build knows but bytes that are not
// this build's pinned identity for the platform (a namespace carried to a
// different architecture, or a rewritten selection) is incompatible, never
// trusted — ATC is the sole authority on the bytes it runs.
func TestSelectionMustMatchPinnedIdentity(t *testing.T) {
	dir := t.TempDir()
	selFile := filepath.Join(t.TempDir(), "runtime.json")
	path, sel := installed(t, dir, "0.6.0")
	// The table pins a different executable identity for 0.6.0 than the
	// one recorded, as a foreign-architecture binary would.
	rt := NewRuntime(RuntimeOptions{RuntimeDir: dir, SelectionFile: selFile,
		Releases: map[string]Release{"0.6.0": {Assets: map[string]Asset{target(): {Name: "n", ArchiveSHA256: "a", ExecutableSHA256: "not-the-recorded-identity"}}}},
		Desired:  "0.6.0"})
	if err := writeSelection(selFile, sel); err != nil {
		t.Fatal(err)
	}
	rt.load()
	_ = path
	if _, err := rt.Executable(); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("Executable() = %v, want an incompatibility refusal", err)
	}
	if rt.Available() {
		t.Error("Available() = true for a non-pinned selection")
	}
}
