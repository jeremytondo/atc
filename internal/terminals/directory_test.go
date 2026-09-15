package terminals

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/terminals/report"
)

func TestDirectoryObservationAndRetention(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	terminal, err := f.create(ctx, api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	want := terminal
	sub := f.hub.Subscribe(0, false)
	defer sub.Close()
	// Consume no historical events; observations alone publish none.
	for len(sub.C) > 0 {
		<-sub.C
	}
	write := func(rep report.Report) {
		t.Helper()
		if err := report.Write(report.Path(f.reports, terminal.ID), rep); err != nil {
			t.Fatal(err)
		}
		f.service.Reconcile(ctx)
	}
	check := func() {
		t.Helper()
		got, err := f.service.Get(terminal.ID)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("terminal (-want +got):\n%s", diff)
		}
	}
	rep := report.Report{TerminalID: terminal.ID, StartedAt: time.Now()}
	write(rep) // Old monitor report: keep the initialized directory.
	check()
	for _, directory := range []string{filepath.Join(f.home, "worktree one"), filepath.Join(f.home, "worktree two")} {
		rep.Directory = directory
		write(rep)
		want.Directory = directory
		check()
	}
	for _, directory := range []string{"", "relative/path"} {
		rep.Directory = directory
		write(rep)
		check()
	}
	rep.Directory = "/stale"
	rep.StartedAt = terminal.CreatedAt.Add(-time.Hour)
	write(rep)
	check()
	if err := os.WriteFile(report.Path(f.reports, terminal.ID), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.service.Reconcile(ctx)
	check()
	if err := report.Remove(f.reports, terminal.ID); err != nil {
		t.Fatal(err)
	}
	f.service.Reconcile(ctx)
	check()
	if len(sub.C) != 0 {
		t.Fatal("directory observations published events")
	}
	// Even a lost report plus a server restart keeps the last observation
	// in the same directory column; no launch-directory copy is needed.
	restarted := NewService(Options{
		Repository: f.service.repository, Driver: f.driver, Spaces: f.service.spaces,
		HomeDir: f.home, ReportDir: f.reports, Hub: events.NewHub(64), Now: f.clock.Now,
	})
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.Reconcile(ctx)
	f.service = restarted
	check()
	f.driver.remove(terminal.ID)
	plantReport(t, f.reports, terminal.ID, 0, true, "")
	f.service.Reconcile(ctx)
	got, _ := f.service.Get(terminal.ID)
	if got.Directory != want.Directory || got.Status != api.TerminalExited {
		t.Errorf("exited terminal = %+v", got)
	}
}
