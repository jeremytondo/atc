package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

// TestWorkspaceMigrationDestroysSessionsPreservesProjects runs the 0003
// migration against a populated 0002 database: session rows are destroyed by
// the rebuild, projects survive, and no manual reset is needed.
func TestWorkspaceMigrationDestroysSessionsPreservesProjects(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "atc.db")

	db, err := sql.Open(sqliteDriver, sqliteDSN(dbPath))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	migrationFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatalf("sub migrations: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationFS, goose.WithLogger(goose.NopLogger()))
	if err != nil {
		t.Fatalf("goose provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 2); err != nil {
		t.Fatalf("migrate to 0002: %v", err)
	}
	if _, err := db.Exec(`
	INSERT INTO projects (id, name, working_dir, created_at, updated_at)
	VALUES ('prj_keep', 'Keep', '/work', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatalf("insert 0002 project: %v", err)
	}
	if _, err := db.Exec(`
	INSERT INTO sessions (id, action, environment, params, working_dir, status, project_id, created_at, updated_at)
	VALUES ('ses_old', 'codex', 'host-login-shell', '{}', '/work', 'ended', 'prj_keep', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatalf("insert 0002 session: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	st := openTestStoreAt(t, dbPath)
	defer st.Close()

	if _, err := st.Get(ctx, "ses_old"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("pre-workspace session survived the rebuild: err = %v, want ErrSessionNotFound", err)
	}
	project, err := st.GetProject(ctx, "prj_keep")
	if err != nil {
		t.Fatalf("GetProject after migration: %v", err)
	}
	if project.Name != "Keep" {
		t.Fatalf("project after migration = %+v", project)
	}

	// The rebuilt schema accepts workspace-scoped sessions.
	if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: "wsp_new", ProjectID: "prj_keep", Name: "New"}); err != nil {
		t.Fatalf("CreateWorkspace after migration: %v", err)
	}
	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_new", Action: "codex", Environment: "host-login-shell", WorkingDir: "/work", WorkspaceID: "wsp_new",
	}); err != nil {
		t.Fatalf("CreateStarting after migration: %v", err)
	}
}

func TestCreateWorkspaceGuardsProject(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: "wsp_orphan", ProjectID: "prj_ghost", Name: "Orphan"}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("create in missing project err = %v, want ErrProjectNotFound", err)
	}

	if _, err := st.CreateProject(ctx, CreateProjectInput{ID: "prj_gone", Name: "Gone", WorkingDir: "/work"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.ArchiveProject(ctx, "prj_gone"); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: "wsp_late", ProjectID: "prj_gone", Name: "Late"}); !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("create in archived project err = %v, want ErrProjectArchived", err)
	}
	// The rejected insert must leave no workspace row behind.
	if _, err := st.GetWorkspace(ctx, "wsp_late"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("GetWorkspace err = %v, want ErrWorkspaceNotFound", err)
	}
}

func TestCreateSessionGuardsWorkspace(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_orphan", Action: "codex", Environment: "host-login-shell", WorkingDir: "/work", WorkspaceID: "wsp_ghost",
	}); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("CreateStarting err = %v, want ErrWorkspaceNotFound", err)
	}

	seedWorkspace(t, st, "prj_home", "wsp_gone")
	if _, err := st.ArchiveWorkspace(ctx, "wsp_gone"); err != nil {
		t.Fatalf("ArchiveWorkspace: %v", err)
	}
	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_late", Action: "codex", Environment: "host-login-shell", WorkingDir: "/work", WorkspaceID: "wsp_gone",
	}); !errors.Is(err, ErrWorkspaceArchived) {
		t.Fatalf("CreateStarting err = %v, want ErrWorkspaceArchived", err)
	}
	// The rejected insert must leave no session row behind.
	if _, err := st.Get(ctx, "ses_late"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get err = %v, want ErrSessionNotFound", err)
	}
}

func TestWorkspaceCRUDRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	clock := newTestClock(time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC))
	st.now = clock.Now

	if _, err := st.CreateProject(ctx, CreateProjectInput{ID: "prj_home", Name: "Home", WorkingDir: "/work"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	created, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: "wsp_alpha", ProjectID: "prj_home", Name: "Login bug"})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if created.ID != "wsp_alpha" || created.ProjectID != "prj_home" || created.Name != "Login bug" {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAt.IsZero() || !created.CreatedAt.Equal(created.UpdatedAt) || created.ArchivedAt != nil {
		t.Fatalf("created timestamps = %+v", created)
	}

	got, err := st.GetWorkspace(ctx, "wsp_alpha")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if !reflect.DeepEqual(got, created) {
		t.Fatalf("GetWorkspace = %+v, want %+v", got, created)
	}

	// Same-named workspaces within one project are allowed.
	if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: "wsp_dup", ProjectID: "prj_home", Name: "Login bug"}); err != nil {
		t.Fatalf("CreateWorkspace same name: %v", err)
	}

	renamed, err := st.RenameWorkspace(ctx, "wsp_alpha", "Login bug v2")
	if err != nil {
		t.Fatalf("RenameWorkspace: %v", err)
	}
	if renamed.Name != "Login bug v2" || !renamed.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("renamed = %+v", renamed)
	}

	archived, err := st.ArchiveWorkspace(ctx, "wsp_alpha")
	if err != nil {
		t.Fatalf("ArchiveWorkspace: %v", err)
	}
	if archived.ArchivedAt == nil || !archived.UpdatedAt.After(renamed.UpdatedAt) {
		t.Fatalf("archived = %+v", archived)
	}
	archivedAgain, err := st.ArchiveWorkspace(ctx, "wsp_alpha")
	if err != nil {
		t.Fatalf("ArchiveWorkspace again: %v", err)
	}
	if !archivedAgain.ArchivedAt.Equal(*archived.ArchivedAt) || !archivedAgain.UpdatedAt.Equal(archived.UpdatedAt) {
		t.Fatalf("second archive = %+v, want unchanged %+v", archivedAgain, archived)
	}

	// Rename stays allowed while archived.
	if _, err := st.RenameWorkspace(ctx, "wsp_alpha", "Renamed while archived"); err != nil {
		t.Fatalf("RenameWorkspace archived: %v", err)
	}

	unarchived, err := st.UnarchiveWorkspace(ctx, "wsp_alpha")
	if err != nil {
		t.Fatalf("UnarchiveWorkspace: %v", err)
	}
	if unarchived.ArchivedAt != nil {
		t.Fatalf("unarchived = %+v", unarchived)
	}
	unarchivedAgain, err := st.UnarchiveWorkspace(ctx, "wsp_alpha")
	if err != nil {
		t.Fatalf("UnarchiveWorkspace again: %v", err)
	}
	if unarchivedAgain.ArchivedAt != nil || !unarchivedAgain.UpdatedAt.Equal(unarchived.UpdatedAt) {
		t.Fatalf("second unarchive = %+v, want unchanged %+v", unarchivedAgain, unarchived)
	}
}

func TestWorkspaceNotFoundErrors(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.GetWorkspace(ctx, "wsp_missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("GetWorkspace err = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := st.RenameWorkspace(ctx, "wsp_missing", "name"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("RenameWorkspace err = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := st.ArchiveWorkspace(ctx, "wsp_missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("ArchiveWorkspace err = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := st.UnarchiveWorkspace(ctx, "wsp_missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("UnarchiveWorkspace err = %v, want ErrWorkspaceNotFound", err)
	}
	if err := st.DeleteWorkspace(ctx, "wsp_missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("DeleteWorkspace err = %v, want ErrWorkspaceNotFound", err)
	}
}

func TestArchiveWorkspaceBlockedByActiveSessions(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_home", "wsp_busy")

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_active", Action: "codex", Environment: "host-login-shell", WorkingDir: "/work", WorkspaceID: "wsp_busy",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}

	// Blocked while the record is provisional, then while the Session is Live.
	if _, err := st.ArchiveWorkspace(ctx, "wsp_busy"); !errors.Is(err, ErrWorkspaceHasActiveSessions) {
		t.Fatalf("archive with starting session err = %v, want ErrWorkspaceHasActiveSessions", err)
	}
	if _, err := st.PromoteToLive(ctx, "ses_active"); err != nil {
		t.Fatalf("PromoteToLive: %v", err)
	}
	if _, err := st.ArchiveWorkspace(ctx, "wsp_busy"); !errors.Is(err, ErrWorkspaceHasActiveSessions) {
		t.Fatalf("archive with Live Session err = %v, want ErrWorkspaceHasActiveSessions", err)
	}
	got, err := st.GetWorkspace(ctx, "wsp_busy")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.ArchivedAt != nil {
		t.Fatalf("workspace archived despite active session: %+v", got)
	}

	// Ended Sessions do not block.
	if _, err := st.MarkEnded(ctx, "ses_active"); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}
	archived, err := st.ArchiveWorkspace(ctx, "wsp_busy")
	if err != nil {
		t.Fatalf("archive with only inactive sessions: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Fatalf("archived = %+v, want archivedAt set", archived)
	}
}

func TestUnarchiveWorkspaceBlockedByArchivedProject(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_home", "wsp_child")

	if _, err := st.ArchiveWorkspace(ctx, "wsp_child"); err != nil {
		t.Fatalf("ArchiveWorkspace: %v", err)
	}
	if _, err := st.ArchiveProject(ctx, "prj_home"); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}

	if _, err := st.UnarchiveWorkspace(ctx, "wsp_child"); !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("unarchive under archived project err = %v, want ErrProjectArchived", err)
	}

	if _, err := st.UnarchiveProject(ctx, "prj_home"); err != nil {
		t.Fatalf("UnarchiveProject: %v", err)
	}
	unarchived, err := st.UnarchiveWorkspace(ctx, "wsp_child")
	if err != nil {
		t.Fatalf("UnarchiveWorkspace after project unarchive: %v", err)
	}
	if unarchived.ArchivedAt != nil {
		t.Fatalf("unarchived = %+v", unarchived)
	}
}

func TestDeleteWorkspaceGuardsAndCascades(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_home", "wsp_doomed")

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_live", Action: "codex", Environment: "host-login-shell", WorkingDir: "/work", WorkspaceID: "wsp_doomed",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if _, err := st.PromoteToLive(ctx, "ses_live"); err != nil {
		t.Fatalf("PromoteToLive: %v", err)
	}

	// The transactional re-check rejects deletion while a session is active,
	// and the rejected delete leaves everything intact.
	if err := st.DeleteWorkspace(ctx, "wsp_doomed"); !errors.Is(err, ErrWorkspaceHasActiveSessions) {
		t.Fatalf("delete with active session err = %v, want ErrWorkspaceHasActiveSessions", err)
	}
	if _, err := st.Get(ctx, "ses_live"); err != nil {
		t.Fatalf("session lost by rejected delete: %v", err)
	}
	if _, err := st.GetWorkspace(ctx, "wsp_doomed"); err != nil {
		t.Fatalf("workspace lost by rejected delete: %v", err)
	}

	if _, err := st.MarkEnded(ctx, "ses_live"); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}
	if err := st.DeleteWorkspace(ctx, "wsp_doomed"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if _, err := st.GetWorkspace(ctx, "wsp_doomed"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("GetWorkspace after delete err = %v, want ErrWorkspaceNotFound", err)
	}
	if _, err := st.Get(ctx, "ses_live"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("session row survived workspace delete: err = %v, want ErrSessionNotFound", err)
	}
	// The project is untouched.
	if _, err := st.GetProject(ctx, "prj_home"); err != nil {
		t.Fatalf("GetProject after workspace delete: %v", err)
	}
}

func TestListWorkspacesFiltersAndOrder(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	clock := newTestClock(time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC))
	st.now = clock.Now

	for _, id := range []string{"prj_one", "prj_two"} {
		if _, err := st.CreateProject(ctx, CreateProjectInput{ID: id, Name: id, WorkingDir: "/work"}); err != nil {
			t.Fatalf("CreateProject(%s): %v", id, err)
		}
	}
	create := func(id, projectID string) {
		t.Helper()
		if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: id, ProjectID: projectID, Name: id}); err != nil {
			t.Fatalf("CreateWorkspace(%s): %v", id, err)
		}
	}
	create("wsp_old", "prj_one")
	create("wsp_middle", "prj_two")
	create("wsp_new", "prj_one")
	if _, err := st.ArchiveWorkspace(ctx, "wsp_middle"); err != nil {
		t.Fatalf("ArchiveWorkspace: %v", err)
	}

	all, err := st.ListWorkspaces(ctx, WorkspaceListFilter{})
	if err != nil {
		t.Fatalf("ListWorkspaces default: %v", err)
	}
	assertWorkspaceIDs(t, all, []string{"wsp_new", "wsp_old"})

	withArchived, err := st.ListWorkspaces(ctx, WorkspaceListFilter{IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListWorkspaces include archived: %v", err)
	}
	assertWorkspaceIDs(t, withArchived, []string{"wsp_new", "wsp_middle", "wsp_old"})

	scoped, err := st.ListWorkspaces(ctx, WorkspaceListFilter{ProjectID: "prj_one"})
	if err != nil {
		t.Fatalf("ListWorkspaces project filter: %v", err)
	}
	assertWorkspaceIDs(t, scoped, []string{"wsp_new", "wsp_old"})
}

func TestSessionWorkspaceHydrationAndFilters(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateProject(ctx, CreateProjectInput{ID: "prj_home", Name: "Home", WorkingDir: "/work/home"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: "wsp_home", ProjectID: "prj_home", Name: "Homework"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	seedWorkspace(t, st, "prj_other", "wsp_other")

	created, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_scoped",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work/home",
		WorkspaceID: "wsp_home",
	})
	if err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if created.WorkspaceID != "wsp_home" || created.Workspace == nil || created.Workspace.Name != "Homework" {
		t.Fatalf("created workspace ref = %+v", created.Workspace)
	}
	if created.Project == nil || created.Project.ID != "prj_home" || created.Project.Name != "Home" {
		t.Fatalf("created project ref = %+v", created.Project)
	}
	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_other", Action: "codex", Environment: "host-login-shell", WorkingDir: "/work", WorkspaceID: "wsp_other",
	}); err != nil {
		t.Fatalf("CreateStarting other: %v", err)
	}
	if _, err := st.PromoteToLive(ctx, "ses_other"); err != nil {
		t.Fatalf("PromoteToLive other: %v", err)
	}

	// Reads hydrate through the join.
	got, err := st.Get(ctx, "ses_scoped")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Workspace == nil || got.Workspace.ID != "wsp_home" || got.Project == nil || got.Project.ID != "prj_home" {
		t.Fatalf("get refs = workspace %+v project %+v", got.Workspace, got.Project)
	}

	// Writes hydrate too, so status transitions keep the refs visible.
	live, err := st.PromoteToLive(ctx, "ses_scoped")
	if err != nil {
		t.Fatalf("PromoteToLive: %v", err)
	}
	if live.Workspace == nil || live.Workspace.Name != "Homework" || live.Project == nil || live.Project.Name != "Home" {
		t.Fatalf("Live refs = workspace %+v project %+v", live.Workspace, live.Project)
	}

	// Workspace and project filtering restrict the list.
	scopedList, err := st.List(ctx, ListFilter{WorkspaceID: "wsp_home"})
	if err != nil {
		t.Fatalf("List workspace filter: %v", err)
	}
	assertSessionIDs(t, scopedList, []string{"ses_scoped"})
	projectList, err := st.List(ctx, ListFilter{ProjectID: "prj_other"})
	if err != nil {
		t.Fatalf("List project filter: %v", err)
	}
	assertSessionIDs(t, projectList, []string{"ses_other"})
	fullList, err := st.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	assertSessionIDs(t, fullList, []string{"ses_other", "ses_scoped"})
}

func assertWorkspaceIDs(t *testing.T, workspaces []Workspace, want []string) {
	t.Helper()
	got := make([]string, len(workspaces))
	for i, workspace := range workspaces {
		got[i] = workspace.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace ids = %v, want %v", got, want)
	}
}
