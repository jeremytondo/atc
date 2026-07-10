package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestMigrationAddsProjectsSchema(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()

	var projectsTable int
	if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'projects'`).Scan(&projectsTable); err != nil {
		t.Fatalf("query projects table: %v", err)
	}
	if projectsTable != 1 {
		t.Fatalf("projects table count = %d, want 1", projectsTable)
	}

	for _, index := range []string{"projects_created_at_idx", "projects_archived_at_idx", "sessions_project_id_idx"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d, want 1", index, count)
		}
	}

	var projectIDColumn int
	if err := st.db.QueryRow(`SELECT count(*) FROM pragma_table_info('sessions') WHERE name = 'project_id'`).Scan(&projectIDColumn); err != nil {
		t.Fatalf("query sessions.project_id column: %v", err)
	}
	if projectIDColumn != 1 {
		t.Fatalf("sessions.project_id column count = %d, want 1", projectIDColumn)
	}
}

func TestCreateSessionEnforcesProjectForeignKey(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_orphan",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
		ProjectID:   "prj_ghost",
	}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("CreateStarting err = %v, want ErrProjectNotFound", err)
	}
}

func TestCreateSessionRejectsArchivedProject(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateProject(ctx, CreateProjectInput{ID: "prj_gone", Name: "Gone", WorkingDir: "/work"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.ArchiveProject(ctx, "prj_gone"); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_late",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
		ProjectID:   "prj_gone",
	}); !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("CreateStarting err = %v, want ErrProjectArchived", err)
	}
	// The rejected insert must leave no session row behind.
	if _, err := st.Get(ctx, "ses_late"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get err = %v, want ErrSessionNotFound", err)
	}
}

func TestArchiveProjectBlockedByActiveSessions(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateProject(ctx, CreateProjectInput{ID: "prj_busy", Name: "Busy", WorkingDir: "/work"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_active",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
		ProjectID:   "prj_busy",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}

	// Blocked while the session is starting, then running.
	if _, err := st.ArchiveProject(ctx, "prj_busy"); !errors.Is(err, ErrProjectHasActiveSessions) {
		t.Fatalf("archive with starting session err = %v, want ErrProjectHasActiveSessions", err)
	}
	if _, err := st.MarkRunning(ctx, "ses_active"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if _, err := st.ArchiveProject(ctx, "prj_busy"); !errors.Is(err, ErrProjectHasActiveSessions) {
		t.Fatalf("archive with running session err = %v, want ErrProjectHasActiveSessions", err)
	}
	// A rejected archive must not have flipped archived_at.
	got, err := st.GetProject(ctx, "prj_busy")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.ArchivedAt != nil {
		t.Fatalf("project archived despite active session: %+v", got)
	}

	// Terminated (even archived) and failed sessions do not block.
	if _, err := st.MarkTerminated(ctx, "ses_active"); err != nil {
		t.Fatalf("MarkTerminated: %v", err)
	}
	if _, err := st.MarkArchived(ctx, "ses_active"); err != nil {
		t.Fatalf("MarkArchived: %v", err)
	}
	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_failed",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
		ProjectID:   "prj_busy",
	}); err != nil {
		t.Fatalf("CreateStarting failed session: %v", err)
	}
	if _, err := st.MarkFailed(ctx, "ses_failed", "launch failed", "launch_failed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	archived, err := st.ArchiveProject(ctx, "prj_busy")
	if err != nil {
		t.Fatalf("archive with only inactive sessions: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Fatalf("archived = %+v, want archivedAt set", archived)
	}
}

func TestProjectCRUDRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	clock := newTestClock(time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC))
	st.now = clock.Now

	created, err := st.CreateProject(ctx, CreateProjectInput{ID: "prj_alpha", Name: "atc", WorkingDir: "/work/atc"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if created.ID != "prj_alpha" || created.Name != "atc" || created.WorkingDir != "/work/atc" {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAt.IsZero() || !created.CreatedAt.Equal(created.UpdatedAt) || created.ArchivedAt != nil {
		t.Fatalf("created timestamps = %+v", created)
	}

	got, err := st.GetProject(ctx, "prj_alpha")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if !reflect.DeepEqual(got, created) {
		t.Fatalf("GetProject = %+v, want %+v", got, created)
	}

	renamed, err := st.RenameProject(ctx, "prj_alpha", "atc v2")
	if err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if renamed.Name != "atc v2" || !renamed.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("renamed = %+v", renamed)
	}

	archived, err := st.ArchiveProject(ctx, "prj_alpha")
	if err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}
	if archived.ArchivedAt == nil || !archived.UpdatedAt.After(renamed.UpdatedAt) {
		t.Fatalf("archived = %+v", archived)
	}
	archivedAgain, err := st.ArchiveProject(ctx, "prj_alpha")
	if err != nil {
		t.Fatalf("ArchiveProject again: %v", err)
	}
	if !archivedAgain.ArchivedAt.Equal(*archived.ArchivedAt) || !archivedAgain.UpdatedAt.Equal(archived.UpdatedAt) {
		t.Fatalf("second archive = %+v, want unchanged %+v", archivedAgain, archived)
	}

	unarchived, err := st.UnarchiveProject(ctx, "prj_alpha")
	if err != nil {
		t.Fatalf("UnarchiveProject: %v", err)
	}
	if unarchived.ArchivedAt != nil || !unarchived.UpdatedAt.After(archived.UpdatedAt) {
		t.Fatalf("unarchived = %+v", unarchived)
	}
	unarchivedAgain, err := st.UnarchiveProject(ctx, "prj_alpha")
	if err != nil {
		t.Fatalf("UnarchiveProject again: %v", err)
	}
	if unarchivedAgain.ArchivedAt != nil || !unarchivedAgain.UpdatedAt.Equal(unarchived.UpdatedAt) {
		t.Fatalf("second unarchive = %+v, want unchanged %+v", unarchivedAgain, unarchived)
	}
}

func TestProjectNotFoundErrors(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.GetProject(ctx, "prj_missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("GetProject err = %v, want ErrProjectNotFound", err)
	}
	if _, err := st.RenameProject(ctx, "prj_missing", "name"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("RenameProject err = %v, want ErrProjectNotFound", err)
	}
	if _, err := st.ArchiveProject(ctx, "prj_missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("ArchiveProject err = %v, want ErrProjectNotFound", err)
	}
	if _, err := st.UnarchiveProject(ctx, "prj_missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("UnarchiveProject err = %v, want ErrProjectNotFound", err)
	}
}

func TestListProjectsOrderingAndArchivedDefault(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	clock := newTestClock(time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC))
	st.now = clock.Now

	for _, id := range []string{"prj_old", "prj_middle", "prj_new"} {
		if _, err := st.CreateProject(ctx, CreateProjectInput{ID: id, Name: id, WorkingDir: "/work"}); err != nil {
			t.Fatalf("CreateProject(%s): %v", id, err)
		}
	}
	if _, err := st.ArchiveProject(ctx, "prj_middle"); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}

	active, err := st.ListProjects(ctx, ProjectListFilter{})
	if err != nil {
		t.Fatalf("ListProjects default: %v", err)
	}
	assertProjectIDs(t, active, []string{"prj_new", "prj_old"})

	all, err := st.ListProjects(ctx, ProjectListFilter{IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListProjects include archived: %v", err)
	}
	assertProjectIDs(t, all, []string{"prj_new", "prj_middle", "prj_old"})
}

func TestListProjectsBreaksCreatedAtTiesByInsertionOrder(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	frozen := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return frozen }

	for _, id := range []string{"prj_c", "prj_a", "prj_b"} {
		if _, err := st.CreateProject(ctx, CreateProjectInput{ID: id, Name: id, WorkingDir: "/work"}); err != nil {
			t.Fatalf("CreateProject(%s): %v", id, err)
		}
	}
	list, err := st.ListProjects(ctx, ProjectListFilter{})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	assertProjectIDs(t, list, []string{"prj_b", "prj_a", "prj_c"})
}

func TestSessionProjectHydrationAndFilter(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateProject(ctx, CreateProjectInput{ID: "prj_home", Name: "Home", WorkingDir: "/work/home"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	scoped, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_scoped",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work/home",
		ProjectID:   "prj_home",
	})
	if err != nil {
		t.Fatalf("CreateStarting scoped: %v", err)
	}
	if scoped.ProjectID != "prj_home" || scoped.Project == nil || scoped.Project.Name != "Home" || scoped.Project.WorkingDir != "/work/home" {
		t.Fatalf("scoped create = %+v project=%+v", scoped, scoped.Project)
	}
	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_unscoped",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
	}); err != nil {
		t.Fatalf("CreateStarting unscoped: %v", err)
	}

	// Reads hydrate through the join.
	got, err := st.Get(ctx, "ses_scoped")
	if err != nil {
		t.Fatalf("Get scoped: %v", err)
	}
	if got.Project == nil || got.Project.ID != "prj_home" || got.Project.ArchivedAt != nil {
		t.Fatalf("scoped get project = %+v", got.Project)
	}
	unscoped, err := st.Get(ctx, "ses_unscoped")
	if err != nil {
		t.Fatalf("Get unscoped: %v", err)
	}
	if unscoped.ProjectID != "" || unscoped.Project != nil {
		t.Fatalf("unscoped get = %+v project=%+v", unscoped, unscoped.Project)
	}

	// Writes hydrate too, so status transitions keep the project visible.
	running, err := st.MarkRunning(ctx, "ses_scoped")
	if err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if running.Project == nil || running.Project.ID != "prj_home" {
		t.Fatalf("running project = %+v", running.Project)
	}

	// The joined project reflects archive state (the session must be inactive
	// first; archiving over an active session is rejected).
	if _, err := st.MarkTerminated(ctx, "ses_scoped"); err != nil {
		t.Fatalf("MarkTerminated: %v", err)
	}
	if _, err := st.ArchiveProject(ctx, "prj_home"); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}
	archivedProject, err := st.Get(ctx, "ses_scoped")
	if err != nil {
		t.Fatalf("Get after project archive: %v", err)
	}
	if archivedProject.Project == nil || archivedProject.Project.ArchivedAt == nil {
		t.Fatalf("project after archive = %+v", archivedProject.Project)
	}

	// ProjectID filtering restricts the list.
	scopedList, err := st.List(ctx, ListFilter{ProjectID: "prj_home"})
	if err != nil {
		t.Fatalf("List filtered: %v", err)
	}
	assertSessionIDs(t, scopedList, []string{"ses_scoped"})
	fullList, err := st.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	assertSessionIDs(t, fullList, []string{"ses_unscoped", "ses_scoped"})
}

func TestPreProjectSessionsReadBackWithNoProject(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	// Rows inserted without a project column value (as every pre-migration row
	// was) must read back cleanly with no project.
	if _, err := st.db.Exec(`
	INSERT INTO sessions (id, action, environment, params, working_dir, status, created_at, updated_at)
	VALUES ('ses_legacy', 'codex', 'host-login-shell', '{}', '/work', 'terminated', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	got, err := st.Get(ctx, "ses_legacy")
	if err != nil {
		t.Fatalf("Get legacy: %v", err)
	}
	if got.ProjectID != "" || got.Project != nil {
		t.Fatalf("legacy session = %+v project=%+v, want no project", got, got.Project)
	}
}

func assertProjectIDs(t *testing.T, projects []Project, want []string) {
	t.Helper()
	got := make([]string, len(projects))
	for i, project := range projects {
		got[i] = project.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("project ids = %v, want %v", got, want)
	}
}
