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

// fakeEnder is a faked SessionEnder recording end calls. When err
// is set the first call fails.
type fakeEnder struct {
	st    *store.Store
	err   error
	ended []string
}

func (f *fakeEnder) End(ctx context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	f.ended = append(f.ended, id)
	record, err := f.st.Get(ctx, id)
	if err != nil {
		return err
	}
	if record.Status == store.StatusStarting {
		return f.st.DeleteStarting(ctx, id)
	}
	if _, err := f.st.MarkEnded(ctx, id); err != nil {
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

func seedSession(t *testing.T, st *store.Store, id, workspaceID string, live bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateStarting(ctx, store.CreateSessionInput{
		ID: id, Action: "codex", Environment: "host-login-shell", WorkingDir: "/work", WorkspaceID: workspaceID,
	}); err != nil {
		t.Fatalf("CreateStarting(%s): %v", id, err)
	}
	if live {
		if _, err := st.PromoteToLive(ctx, id); err != nil {
			t.Fatalf("PromoteToLive(%s): %v", id, err)
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

}

func TestDeleteStopsActiveSessionsThenRemovesMetadata(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	seedProject(t, st, "prj_home", "/work")
	ws, err := svc.Create(ctx, "prj_home", "Doomed")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedSession(t, st, "ses_live", ws.ID, true)
	seedSession(t, st, "ses_done", ws.ID, true)
	if _, err := st.MarkEnded(ctx, "ses_done"); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}

	ender := &fakeEnder{st: st}
	if err := svc.Delete(ctx, ws.ID, ender); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Only the Live Session is ended; Ended Sessions are left alone.
	if len(ender.ended) != 1 || ender.ended[0] != "ses_live" {
		t.Fatalf("ended = %v, want only ses_live", ender.ended)
	}
	if _, err := svc.Get(ctx, ws.ID); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := st.Get(ctx, "ses_done"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("session metadata survived delete: %v", err)
	}
}

func TestDeleteEndFailureAbortsWithMetadataIntact(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	seedProject(t, st, "prj_home", "/work")
	ws, err := svc.Create(ctx, "prj_home", "Sticky")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedSession(t, st, "ses_stuck", ws.ID, true)

	ender := &fakeEnder{st: st, err: errors.New("zmx kill failed")}
	err = svc.Delete(ctx, ws.ID, ender)
	if !errors.Is(err, ErrSessionEndFailed) {
		t.Fatalf("Delete err = %v, want ErrSessionEndFailed", err)
	}
	// Nothing was deleted: workspace and session rows are intact.
	if _, err := svc.Get(ctx, ws.ID); err != nil {
		t.Fatalf("workspace lost after aborted delete: %v", err)
	}
	if _, err := st.Get(ctx, "ses_stuck"); err != nil {
		t.Fatalf("session lost after aborted delete: %v", err)
	}

	if err := svc.Delete(ctx, "wsp_missing", ender); !errors.Is(err, ErrWorkspaceNotFound) {
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

	// A project directory that vanished since creation fails fast.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	if _, err := svc.ResolveForStart(ctx, ws.ID); !errors.Is(err, project.ErrInvalidWorkingDir) {
		t.Fatalf("vanished dir err = %v, want ErrInvalidWorkingDir", err)
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

	all, err := svc.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %+v, want 2", all)
	}
	scoped, err := svc.List(ctx, "prj_one")
	if err != nil {
		t.Fatalf("List scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].ID != first.ID {
		t.Fatalf("scoped = %+v, want only %s", scoped, first.ID)
	}
	_ = second
}
