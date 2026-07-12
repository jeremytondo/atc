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

	for _, index := range []string{"projects_created_at_idx", "projects_archived_at_idx"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d, want 1", index, count)
		}
	}
}

func TestArchiveProjectBlockedByUnarchivedWorkspaces(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateProject(ctx, CreateProjectInput{ID: "prj_busy", Name: "Busy", WorkingDir: "/work"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: "wsp_open", ProjectID: "prj_busy", Name: "Open"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	if _, err := st.ArchiveProject(ctx, "prj_busy"); !errors.Is(err, ErrProjectHasUnarchivedWorkspaces) {
		t.Fatalf("archive with unarchived workspace err = %v, want ErrProjectHasUnarchivedWorkspaces", err)
	}
	// A rejected archive must not have flipped archived_at.
	got, err := st.GetProject(ctx, "prj_busy")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.ArchivedAt != nil {
		t.Fatalf("project archived despite unarchived workspace: %+v", got)
	}

	// Once every workspace is archived, the project can archive. Ended
	// sessions inside archived workspaces do not block.
	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_done", Action: "codex", Environment: "host-login-shell", WorkingDir: "/work", WorkspaceID: "wsp_open",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if _, err := st.MarkFailed(ctx, "ses_done", "launch failed", "launch_failed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if _, err := st.ArchiveWorkspace(ctx, "wsp_open"); err != nil {
		t.Fatalf("ArchiveWorkspace: %v", err)
	}
	archived, err := st.ArchiveProject(ctx, "prj_busy")
	if err != nil {
		t.Fatalf("archive with only archived workspaces: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Fatalf("archived = %+v, want archivedAt set", archived)
	}
}

func TestDeleteProjectRequiresZeroWorkspaces(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if err := st.DeleteProject(ctx, "prj_missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("delete missing project err = %v, want ErrProjectNotFound", err)
	}

	if _, err := st.CreateProject(ctx, CreateProjectInput{ID: "prj_full", Name: "Full", WorkingDir: "/work"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: "wsp_only", ProjectID: "prj_full", Name: "Only"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := st.DeleteProject(ctx, "prj_full"); !errors.Is(err, ErrProjectHasWorkspaces) {
		t.Fatalf("delete project with workspaces err = %v, want ErrProjectHasWorkspaces", err)
	}
	// Archived workspaces still block deletion; only zero workspaces allows it.
	if _, err := st.ArchiveWorkspace(ctx, "wsp_only"); err != nil {
		t.Fatalf("ArchiveWorkspace: %v", err)
	}
	if err := st.DeleteProject(ctx, "prj_full"); !errors.Is(err, ErrProjectHasWorkspaces) {
		t.Fatalf("delete project with archived workspace err = %v, want ErrProjectHasWorkspaces", err)
	}

	if err := st.DeleteWorkspace(ctx, "wsp_only"); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if err := st.DeleteProject(ctx, "prj_full"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := st.GetProject(ctx, "prj_full"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("GetProject after delete err = %v, want ErrProjectNotFound", err)
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
