package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestOpenCreatesBaselineAndSeedsActions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state", "atc.db")
	st := openTestStoreAt(t, dbPath)
	defer st.Close()

	info, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("parent mode = %#o, want 0700", got)
	}
	actions, err := st.ListActions(context.Background())
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if len(actions) != 2 || actions[0].Name != "Claude" || actions[1].Name != "Codex" {
		t.Fatalf("seed actions = %+v", actions)
	}
	if actions[0].ID != "act_vpj2tlg9viqd8ms52ptuvao5c4" || actions[1].ID != "act_fh9g7e6571qo53r0t647ughtfg" {
		t.Fatalf("seed ids = %q, %q", actions[0].ID, actions[1].ID)
	}
}

func TestDeletedSeedIsNotRecreatedOnReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atc.db")
	st := openTestStoreAt(t, path)
	if err := st.DeleteAction(ctx, "act_vpj2tlg9viqd8ms52ptuvao5c4"); err != nil {
		t.Fatalf("DeleteAction: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestStoreAt(t, path)
	defer reopened.Close()
	if _, err := reopened.GetAction(ctx, "act_vpj2tlg9viqd8ms52ptuvao5c4"); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("deleted seed after reopen err = %v, want ErrActionNotFound", err)
	}
}

func TestActionCRUDAllowsDuplicateNamesAndPartialUpdates(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	first, err := st.CreateAction(ctx, Action{Name: "Tool", Description: "first", Enabled: true, Command: "tool", Args: []string{"a"}})
	if err != nil {
		t.Fatalf("CreateAction first: %v", err)
	}
	second, err := st.CreateAction(ctx, Action{Name: "Tool", Enabled: true, Command: "other"})
	if err != nil {
		t.Fatalf("CreateAction duplicate name: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("generated duplicate action ids")
	}

	enabled := false
	updated, err := st.UpdateAction(ctx, first.ID, UpdateActionInput{
		DescriptionSet: true,
		Description:    nil,
		Enabled:        &enabled,
	})
	if err != nil {
		t.Fatalf("UpdateAction: %v", err)
	}
	if updated.Description != "" || updated.Enabled || updated.Name != "Tool" || updated.Command != "tool" || !reflect.DeepEqual(updated.Args, []string{"a"}) {
		t.Fatalf("updated = %+v", updated)
	}

	list, err := st.ListActions(ctx)
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	var duplicates int
	for _, action := range list {
		if action.Name == "Tool" {
			duplicates++
		}
		if action.Args == nil {
			t.Fatalf("nil args in action %+v", action)
		}
	}
	if duplicates != 2 {
		t.Fatalf("duplicate named actions = %d, want 2", duplicates)
	}
	if err := st.DeleteAction(ctx, first.ID); err != nil {
		t.Fatalf("DeleteAction: %v", err)
	}
	if _, err := st.GetAction(ctx, first.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("GetAction deleted err = %v", err)
	}
}

func TestDeleteActionLeavesLiveSessionSnapshot(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")

	action, err := st.CreateAction(ctx, Action{Name: "Editor", Enabled: true, Command: "nvim"})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	created, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_editor", ActionID: action.ID, ActionName: action.Name, WorkingDir: "/work", WorkspaceID: "wsp_main",
	})
	if err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if _, err := st.PromoteToLive(ctx, created.ID); err != nil {
		t.Fatalf("PromoteToLive: %v", err)
	}
	if err := st.DeleteAction(ctx, action.ID); err != nil {
		t.Fatalf("DeleteAction: %v", err)
	}
	got, err := st.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if got.ActionID != action.ID || got.ActionName != "Editor" || got.Status != StatusLive {
		t.Fatalf("session snapshot = %+v", got)
	}
}

func TestSessionLifecycleRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")
	clock := newTestClock(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC))
	st.now = clock.Now

	created, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_alpha", Name: "build", ActionID: "act_x", ActionName: "Codex", IsAgent: true,
		WorkingDir: "/work", WorkspaceID: "wsp_main",
	})
	if err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if created.Status != StatusStarting || created.ActionID != "act_x" || created.ActionName != "Codex" || !created.IsAgent {
		t.Fatalf("created = %+v", created)
	}
	live, err := st.PromoteToLive(ctx, created.ID)
	if err != nil || live.Status != StatusLive {
		t.Fatalf("PromoteToLive = %+v, %v", live, err)
	}
	ended, err := st.MarkEnded(ctx, created.ID)
	if err != nil || ended.Status != StatusEnded {
		t.Fatalf("MarkEnded = %+v, %v", ended, err)
	}
	if err := st.DeleteSession(ctx, created.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := st.Get(ctx, created.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get deleted err = %v", err)
	}
}

func TestInteractiveShellStoresNullActionIdentity(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")

	created, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_shell", WorkingDir: "/work", WorkspaceID: "wsp_main",
	})
	if err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if created.ActionID != "" || created.ActionName != "" || created.IsAgent {
		t.Fatalf("interactive shell identity = %+v", created)
	}
	var actionID, actionName any
	if err := st.db.QueryRow(`SELECT action_id, action_name FROM sessions WHERE id = ?`, created.ID).Scan(&actionID, &actionName); err != nil {
		t.Fatalf("query identity: %v", err)
	}
	if actionID != nil || actionName != nil {
		t.Fatalf("stored identity = %#v, %#v; want NULL", actionID, actionName)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	return openTestStoreAt(t, filepath.Join(t.TempDir(), "atc.db"))
}

func openTestStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st
}

func seedWorkspace(t *testing.T, st *Store, projectID, workspaceID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, CreateProjectInput{ID: projectID, Name: projectID, WorkingDir: "/work"}); err != nil {
		t.Fatalf("CreateProject(%s): %v", projectID, err)
	}
	if _, err := st.CreateWorkspace(ctx, CreateWorkspaceInput{ID: workspaceID, ProjectID: projectID, Name: workspaceID}); err != nil {
		t.Fatalf("CreateWorkspace(%s): %v", workspaceID, err)
	}
}

func assertSessionIDs(t *testing.T, sessions []Session, want []string) {
	t.Helper()
	got := make([]string, len(sessions))
	for i, session := range sessions {
		got[i] = session.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session ids = %v, want %v", got, want)
	}
}

type testClock struct {
	next time.Time
}

func newTestClock(start time.Time) *testClock {
	return &testClock{next: start}
}

func (c *testClock) Now() time.Time {
	t := c.next
	c.next = c.next.Add(time.Second)
	return t
}
