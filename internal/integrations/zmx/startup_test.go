package zmx

// Startup installation and activation tests (ATC-324): the background
// routine that installs and activates beside the serving API. Placement
// is forced non-scoped so the emptiness proofs never reach systemd; the
// real-supervisor seam covers the scoped path.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/placement"
)

// controlledDriver builds a Driver over a controlled runtime and an empty
// private socket directory, with placement forced off.
func controlledDriver(t *testing.T, rt *Runtime) *Driver {
	t.Helper()
	driver, err := New(Options{
		SocketDir:         mkShortTempDir(t),
		ReportDir:         t.TempDir(),
		MonitorExecutable: "/bin/true",
		Runtime:           rt,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	driver.placement = &placement.Host{} // non-scoped: no systemd in the proof
	return driver
}

func servedRuntime(t *testing.T) (*Runtime, *releaseServer) {
	t.Helper()
	exe := fakeZmx(testVersion)
	archive := tarball(t, entry{name: "zmx-dir/zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "zmx-"+testVersion+".tar.gz", archive)
	return controlledRuntime(t, rs, sha256hex(exe)), rs
}

// A fresh empty namespace: startup installs the desired runtime and
// activates it, and the driver then resolves it.
func TestStartupActivatesFreshNamespace(t *testing.T) {
	rt, _ := servedRuntime(t)
	d := controlledDriver(t, rt)
	d.Startup(context.Background(), func() bool { return false })

	executable, err := d.runtime.Executable()
	if err != nil {
		t.Fatalf("after startup Executable() = %v", err)
	}
	if sha256File(t, executable) != rt.releases[testVersion].Assets[target()].ExecutableSHA256 {
		t.Error("activated executable is not the installed one")
	}
	status := d.runtime.Status()
	if status.Active != testVersion || status.Pending {
		t.Errorf("status = %+v, want active %s not pending", status, testVersion)
	}
}

// Incomplete first-rollout cleanup: an unresolved terminal record blocks
// activation without any protocol probing or deletion, and the namespace
// stays without a runtime.
func TestStartupBlocksOnIncompleteCleanup(t *testing.T) {
	rt, _ := servedRuntime(t)
	d := controlledDriver(t, rt)
	d.Startup(context.Background(), func() bool { return true }) // a live record remains

	if _, err := d.runtime.Executable(); err == nil {
		t.Fatal("activated a runtime while cleanup was incomplete")
	}
	status := d.runtime.Status()
	if !status.Pending || status.Active != "" {
		t.Errorf("status = %+v, want pending with no active runtime", status)
	}
	if got := d.runtime.Status().Detail; got == "" {
		t.Error("no detail explaining the blocked activation")
	}
}

// A failed desired-version install leaves a usable, supported active
// runtime available; the failure is reported and the active version keeps
// serving.
func TestStartupFailedDesiredKeepsActive(t *testing.T) {
	dir := t.TempDir()
	selFile := filepath.Join(mkShortTempDir(t), "runtime.json")
	activePath, sel := installed(t, dir, "0.6.0")
	// Desired 0.7.0's install fails: the served archive's sum will not
	// match the table entry.
	exe := fakeZmx("0.7.0")
	archive := tarball(t, entry{name: "zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "bad.tar.gz", archive)
	rt := NewRuntime(RuntimeOptions{RuntimeDir: dir, SelectionFile: selFile,
		Releases: map[string]Release{
			"0.6.0": {Assets: map[string]Asset{target(): {Name: "a", ArchiveSHA256: "x", ExecutableSHA256: sel.SHA256}}},
			"0.7.0": {Assets: map[string]Asset{target(): {Name: "bad.tar.gz", ArchiveSHA256: "deadbeef", ExecutableSHA256: sha256hex(exe)}}},
		}, Desired: "0.7.0", DownloadBase: rs.URL + "/"})
	if err := writeSelection(selFile, sel); err != nil {
		t.Fatal(err)
	}
	rt.load()
	d := controlledDriver(t, rt)
	d.Startup(context.Background(), func() bool { return false })

	got, err := d.runtime.Executable()
	if err != nil || got != activePath {
		t.Fatalf("Executable() = %q, %v; want the active %q to keep serving", got, err, activePath)
	}
	status := d.runtime.Status()
	if status.Active != "0.6.0" || !status.Pending || status.Detail == "" {
		t.Errorf("status = %+v, want active 0.6.0 pending with the failure detail", status)
	}
}

// A missing active executable is reinstalled at startup by its exact
// recorded identity, and the runtime becomes usable again.
func TestStartupRepairsDamagedActive(t *testing.T) {
	exe := fakeZmx(testVersion)
	archive := tarball(t, entry{name: "zmx-dir/zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "zmx-"+testVersion+".tar.gz", archive)
	dir := t.TempDir()
	selFile := filepath.Join(mkShortTempDir(t), "runtime.json")
	rt := NewRuntime(RuntimeOptions{RuntimeDir: dir, SelectionFile: selFile,
		Releases:     map[string]Release{testVersion: {Assets: map[string]Asset{target(): {Name: rs.asset, ArchiveSHA256: sha256hex(archive), ExecutableSHA256: sha256hex(exe)}}}},
		Desired:      testVersion,
		DownloadBase: rs.URL + "/"})
	// First install and activate, then damage the executable.
	executable, err := rt.Install(context.Background(), testVersion)
	if err != nil {
		t.Fatal(err)
	}
	sel, _ := selectionFor(testVersion, executable)
	if err := writeSelection(selFile, sel); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	rt.load()
	if _, err := rt.Executable(); err == nil {
		t.Fatal("Executable() usable with the file removed")
	}
	d := controlledDriver(t, rt)
	d.Startup(context.Background(), func() bool { return false })

	got, err := rt.Executable()
	if err != nil || got != executable {
		t.Fatalf("after repair Executable() = %q, %v; want the exact recorded %q", got, err, executable)
	}
	if sha256File(t, got) != sel.SHA256 {
		t.Error("repaired executable does not match the recorded identity")
	}
}

// A slow startup install never blocks a runtime resolution: Executable()
// returns promptly with an unavailable reason while the download hangs,
// and a later restart retries.
func TestStartupSlowInstallDoesNotBlockResolution(t *testing.T) {
	rt, rs := servedRuntime(t)
	rs.hang = true
	d := controlledDriver(t, rt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Startup(ctx, func() bool { return false }); close(done) }()

	// Resolution stays responsive while the install hangs.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := d.runtime.Executable(); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Executable() never reported unavailable during a hanging install")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(startupBudget):
		t.Fatal("Startup did not return after cancellation")
	}
	if _, err := d.runtime.Executable(); err == nil {
		t.Fatal("a cancelled install left a usable runtime")
	}

	// Restart retries: with the server no longer hanging, startup
	// completes and activates.
	rs.mu.Lock()
	rs.hang = false
	rs.mu.Unlock()
	d.Startup(context.Background(), func() bool { return false })
	if _, err := d.runtime.Executable(); err != nil {
		t.Fatalf("restart did not retry the failed install: %v", err)
	}
}

var _ = errors.Is
