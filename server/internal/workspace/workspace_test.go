package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/store"
)

// fakeStopper is a faked SessionStopper recording terminate calls. When err
// is set the first call fails.
type fakeStopper struct {
	st      *store.Store
	err     error
	stopped []string
}

func (f *fakeStopper) Terminate(ctx context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	f.stopped = append(f.stopped, id)
	if _, err := f.st.MarkTerminated(ctx, id); err != nil {
		return err
	}
	return nil
}

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewService(st, nil), st
}

func seedProject(t *testing.T, st *store.Store, id, dir string) {
	t.Helper()
	if _, err := st.CreateProject(context.Background(), store.CreateProjectInput{ID: id, Name: id, WorkingDir: dir}); err != nil {
		t.Fatalf("CreateProject(%s): %v", id, err)
	}
}

func seedSession(t *testing.T, st *store.Store, id, workspaceID string, running bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateStarting(ctx, store.CreateSessionInput{
		ID: id, Action: "codex", Environment: "host-login-shell", WorkingDir: "/work", WorkspaceID: workspaceID,
	}); err != nil {
		t.Fatalf("CreateStarting(%s): %v", id, err)
	}
	if running {
		if _, err := st.MarkRunning(ctx, id); err != nil {
			t.Fatalf("MarkRunning(%s): %v", id, err)
		}
	}
}

func TestCreateValidatesAndGeneratesID(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	seedProject(t, st, "prj_home", "/work")

	created, err := svc.Create(ctx, "prj_home", "  Login bug  ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.ID) != len("wsp_")+26 || created.ID[:4] != "wsp_" {
		t.Fatalf("id = %q, want wsp_-prefixed public id", created.ID)
	}
	if created.Name != "Login bug" || created.ProjectID != "prj_home" {
		t.Fatalf("created = %+v", created)
	}

	// Two workspaces in one project may share a name.
	if _, err := svc.Create(ctx, "prj_home", "Login bug"); err != nil {
		t.Fatalf("Create same name: %v", err)
	}

	if _, err := svc.Create(ctx, "prj_home", "   "); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("blank name err = %v, want ErrInvalidWorkspace", err)
	}
	if _, err := svc.Create(ctx, "", "Name"); !errors.Is(err, ErrInvalidWorkspace) {
		t.Fatalf("blank project err = %v, want ErrInvalidWorkspace", err)
	}
	if _, err := svc.Create(ctx, "prj_ghost", "Name"); !errors.Is(err, project.ErrProjectNotFound) {
		t.Fatalf("missing project err = %v, want ErrProjectNotFound", err)
	}

	if _, err := st.ArchiveProject(ctx, "prj_other"); !errors.Is(err, store.ErrProjectNotFound) {
		t.Fatalf("sanity: %v", err)
	}
	seedProject(t, st, "prj_gone", "/work")
	if _, err := st.ArchiveProject(ctx, "prj_gone"); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}
	if _, err := svc.Create(ctx, "prj_gone", "Name"); !errors.Is(err, project.ErrProjectArchived) {
		t.Fatalf("archived project err = %v, want ErrProjectArchived", err)
	}
}

func TestDeleteStopsActiveSessionsThenRemovesMetadata(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	seedProject(t, st, "prj_home", "/work")
	ws, err := svc.Create(ctx, "prj_home", "Doomed")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedSession(t, st, "ses_running", ws.ID, true)
	seedSession(t, st, "ses_done", ws.ID, true)
	if _, err := st.MarkTerminated(ctx, "ses_done"); err != nil {
		t.Fatalf("MarkTerminated: %v", err)
	}

	stopper := &fakeStopper{st: st}
	if err := svc.Delete(ctx, ws.ID, stopper); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Only the active session is stopped; settled sessions are left alone.
	if len(stopper.stopped) != 1 || stopper.stopped[0] != "ses_running" {
		t.Fatalf("stopped = %v, want only ses_running", stopper.stopped)
	}
	if _, err := svc.Get(ctx, ws.ID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := st.Get(ctx, "ses_done"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("session metadata survived delete: %v", err)
	}
}

func TestDeleteStopFailureAbortsWithMetadataIntact(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	seedProject(t, st, "prj_home", "/work")
	ws, err := svc.Create(ctx, "prj_home", "Sticky")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedSession(t, st, "ses_stuck", ws.ID, true)

	stopper := &fakeStopper{st: st, err: errors.New("zmx kill failed")}
	err = svc.Delete(ctx, ws.ID, stopper)
	if !errors.Is(err, ErrSessionStopFailed) {
		t.Fatalf("Delete err = %v, want ErrSessionStopFailed", err)
	}
	// Nothing was deleted: workspace and session rows are intact.
	if _, err := svc.Get(ctx, ws.ID); err != nil {
		t.Fatalf("workspace lost after aborted delete: %v", err)
	}
	if _, err := st.Get(ctx, "ses_stuck"); err != nil {
		t.Fatalf("session lost after aborted delete: %v", err)
	}

	if err := svc.Delete(ctx, "wsp_missing", stopper); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("delete missing err = %v, want ErrWorkspaceNotFound", err)
	}
}

func TestResolveForStartChecksChainAndDirectory(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	dir := t.TempDir()
	seedProject(t, st, "prj_home", dir)
	ws, err := svc.Create(ctx, "prj_home", "Work")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resolved, err := svc.ResolveForStart(ctx, ws.ID)
	if err != nil {
		t.Fatalf("ResolveForStart: %v", err)
	}
	if resolved != dir {
		t.Fatalf("resolved dir = %q, want %q", resolved, dir)
	}

	if _, err := svc.ResolveForStart(ctx, "wsp_ghost"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("missing workspace err = %v, want ErrWorkspaceNotFound", err)
	}

	if _, err := svc.Archive(ctx, ws.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, err := svc.ResolveForStart(ctx, ws.ID); !errors.Is(err, ErrWorkspaceArchived) {
		t.Fatalf("archived workspace err = %v, want ErrWorkspaceArchived", err)
	}
	if _, err := svc.Unarchive(ctx, ws.ID); err != nil {
		t.Fatalf("Unarchive: %v", err)
	}

	// A project directory that vanished since creation fails fast.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	if _, err := svc.ResolveForStart(ctx, ws.ID); !errors.Is(err, project.ErrInvalidWorkingDir) {
		t.Fatalf("vanished dir err = %v, want ErrInvalidWorkingDir", err)
	}
}

func TestArchiveUnarchiveRulesSurfaceDomainErrors(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	seedProject(t, st, "prj_home", "/work")
	ws, err := svc.Create(ctx, "prj_home", "Rules")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedSession(t, st, "ses_live", ws.ID, true)

	if _, err := svc.Archive(ctx, ws.ID); !errors.Is(err, ErrWorkspaceHasActiveSessions) {
		t.Fatalf("archive err = %v, want ErrWorkspaceHasActiveSessions", err)
	}
	if _, err := st.MarkTerminated(ctx, "ses_live"); err != nil {
		t.Fatalf("MarkTerminated: %v", err)
	}
	if _, err := svc.Archive(ctx, ws.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, err := st.ArchiveProject(ctx, "prj_home"); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}
	if _, err := svc.Unarchive(ctx, ws.ID); !errors.Is(err, project.ErrProjectArchived) {
		t.Fatalf("unarchive err = %v, want ErrProjectArchived", err)
	}

	// Rename works while archived.
	renamed, err := svc.Rename(ctx, ws.ID, "Renamed")
	if err != nil {
		t.Fatalf("Rename archived: %v", err)
	}
	if renamed.Name != "Renamed" {
		t.Fatalf("renamed = %+v", renamed)
	}
}

func TestListScopesToProject(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	seedProject(t, st, "prj_one", "/work")
	seedProject(t, st, "prj_two", "/work")
	first, err := svc.Create(ctx, "prj_one", "First")
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := svc.Create(ctx, "prj_two", "Second")
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	all, err := svc.List(ctx, false, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %+v, want 2", all)
	}
	scoped, err := svc.List(ctx, false, "prj_one")
	if err != nil {
		t.Fatalf("List scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].ID != first.ID {
		t.Fatalf("scoped = %+v, want only %s", scoped, first.ID)
	}
	_ = second
}
