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

	for _, index := range []string{"projects_created_at_idx"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d, want 1", index, count)
		}
	}
	for _, table := range []string{"projects", "workspaces"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = 'archived_at'`, table).Scan(&count); err != nil {
			t.Fatalf("query %s columns: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s still has archived_at", table)
		}
	}
	for _, index := range []string{"projects_archived_at_idx", "workspaces_archived_at_idx"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
			t.Fatalf("query removed index %s: %v", index, err)
		}
		if count != 0 {
			t.Fatalf("removed index %s still exists", index)
		}
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
	if created.CreatedAt.IsZero() || !created.CreatedAt.Equal(created.UpdatedAt) {
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
}

func TestListProjectsOrdering(t *testing.T) {
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
	projects, err := st.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	assertProjectIDs(t, projects, []string{"prj_new", "prj_middle", "prj_old"})
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
	list, err := st.ListProjects(ctx)
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
