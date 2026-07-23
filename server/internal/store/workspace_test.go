package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

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
	if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: "wsp_child", ProjectID: "prj_gone", Name: "Child"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
}

func TestCreateSessionGuardsWorkspace(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_orphan", ActionID: "act_codex", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_ghost",
	}); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("CreateStarting err = %v, want ErrWorkspaceNotFound", err)
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
	if created.CreatedAt.IsZero() || !created.CreatedAt.Equal(created.UpdatedAt) {
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
	if err := st.DeleteWorkspace(ctx, "wsp_missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("DeleteWorkspace err = %v, want ErrWorkspaceNotFound", err)
	}
}

func TestDeleteWorkspaceGuardsAndCascades(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_home", "wsp_doomed")

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_live", ActionID: "act_codex", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_doomed",
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
	all, err := st.ListWorkspaces(ctx, WorkspaceListFilter{})
	if err != nil {
		t.Fatalf("ListWorkspaces default: %v", err)
	}
	assertWorkspaceIDs(t, all, []string{"wsp_new", "wsp_middle", "wsp_old"})

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
		ActionID:    "act_codex",
		ActionName:  "Codex",
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
		ID: "ses_other", ActionID: "act_codex", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_other",
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
