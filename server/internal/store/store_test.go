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

func TestOpenCreatesParentAndMigrates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state", "atc.db")
	st := openTestStoreAt(t, dbPath)
	defer st.Close()

	info, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("parent dir mode = %#o, want 0700", got)
	}

	var sessionsTable int
	if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`).Scan(&sessionsTable); err != nil {
		t.Fatalf("query sessions table: %v", err)
	}
	if sessionsTable != 1 {
		t.Fatalf("sessions table count = %d, want 1", sessionsTable)
	}
	for _, removed := range []string{"failure_reason", "failure_code", "terminated_at", "archived_at"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM pragma_table_info('sessions') WHERE name = ?`, removed).Scan(&count); err != nil {
			t.Fatalf("query removed column %s: %v", removed, err)
		}
		if count != 0 {
			t.Fatalf("removed session column %s still exists", removed)
		}
	}
	var archiveIndex int
	if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'sessions_archived_at_idx'`).Scan(&archiveIndex); err != nil {
		t.Fatalf("query removed session archive index: %v", err)
	}
	if archiveIndex != 0 {
		t.Fatal("sessions_archived_at_idx still exists")
	}

	var foreignKeys int
	if err := st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := st.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout pragma: %v", err)
	}
	if busyTimeout <= 0 {
		t.Fatalf("busy_timeout = %d, want positive", busyTimeout)
	}
}

func TestOpenFailsWhenParentCannotBeCreated(t *testing.T) {
	// A regular file standing where a parent directory must be created forces
	// MkdirAll to fail, so Open must surface a clear error rather than panic.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	if _, err := Open(filepath.Join(blocker, "atc.db")); err == nil {
		t.Fatal("Open succeeded with a file where the parent dir should be, want error")
	}
}

func TestOpenDoesNotChmodExistingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("chmod parent dir: %v", err)
	}

	st := openTestStoreAt(t, filepath.Join(parent, "atc.db"))
	defer st.Close()

	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("parent dir mode = %#o, want unchanged 0755", got)
	}
}

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
	if created.CreatedAt.IsZero() || !created.CreatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("timestamps = created %s updated %s, want equal non-zero", created.CreatedAt, created.UpdatedAt)
	}
	live, err := st.PromoteToLive(ctx, created.ID)
	if err != nil {
		t.Fatalf("PromoteToLive: %v", err)
	}
	if live.Status != StatusLive {
		t.Fatalf("status = %s, want %s", live.Status, StatusLive)
	}
	if !live.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("updatedAt = %s, want after %s", live.UpdatedAt, created.UpdatedAt)
	}
	ended, err := st.MarkEnded(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}
	if ended.Status != StatusEnded {
		t.Fatalf("status = %s, want %s", ended.Status, StatusEnded)
	}
	endedAgain, err := st.MarkEnded(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkEnded again: %v", err)
	}
	if !endedAgain.UpdatedAt.Equal(ended.UpdatedAt) {
		t.Fatalf("second end updatedAt = %s, want preserved %s", endedAgain.UpdatedAt, ended.UpdatedAt)
	}
	got, err := st.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, ended) {
		t.Fatalf("Get = %+v, want %+v", got, ended)
	}
}

func TestRenameSessionPersistsAndReturnsHydratedRecord(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")
	clock := newTestClock(time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	st.now = clock.Now

	created, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_rename", Name: "Before", WorkingDir: "/work", WorkspaceID: "wsp_main",
	})
	if err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if _, err := st.PromoteToLive(ctx, created.ID); err != nil {
		t.Fatalf("PromoteToLive: %v", err)
	}
	renamed, err := st.RenameSession(ctx, created.ID, "After")
	if err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if renamed.Name != "After" || !renamed.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("renamed = %+v", renamed)
	}
	if renamed.Workspace == nil || renamed.Workspace.ID != "wsp_main" || renamed.Project == nil || renamed.Project.ID != "prj_main" {
		t.Fatalf("refs = workspace %+v project %+v", renamed.Workspace, renamed.Project)
	}
	got, err := st.Get(ctx, created.ID)
	if err != nil || got.Name != "After" {
		t.Fatalf("Get = %+v err=%v", got, err)
	}
	if _, err := st.MarkEnded(ctx, created.ID); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}
	if _, err := st.RenameSession(ctx, created.ID, "After ending"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("rename ended err = %v, want ErrSessionNotFound", err)
	}
	if _, err := st.RenameSession(ctx, "ses_missing", "After"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing err = %v, want ErrSessionNotFound", err)
	}
}

func TestCreateStartingRequiresWorkspace(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_unscoped", ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work",
	}); err == nil {
		t.Fatal("CreateStarting accepted a session without a workspace")
	}
}

func TestDeleteSessionRemovesSettledAndRejectsActive(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_doomed", ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_main",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if err := st.DeleteSession(ctx, "ses_doomed"); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("delete starting session err = %v, want ErrSessionActive", err)
	}
	if _, err := st.PromoteToLive(ctx, "ses_doomed"); err != nil {
		t.Fatalf("PromoteToLive: %v", err)
	}
	if err := st.DeleteSession(ctx, "ses_doomed"); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("delete Live Session err = %v, want ErrSessionActive", err)
	}
	if _, err := st.MarkEnded(ctx, "ses_doomed"); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}
	if err := st.DeleteSession(ctx, "ses_doomed"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := st.Get(ctx, "ses_doomed"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrSessionNotFound", err)
	}
	if err := st.DeleteSession(ctx, "ses_doomed"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("delete missing session err = %v, want ErrSessionNotFound", err)
	}
}

func TestOpenReopensExistingDatabase(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state", "atc.db")
	st := openTestStoreAt(t, dbPath)
	seedWorkspace(t, st, "prj_main", "wsp_main")
	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_persisted", ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_main",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestStoreAt(t, dbPath)
	defer reopened.Close()
	got, err := reopened.Get(ctx, "ses_persisted")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.ID != "ses_persisted" || got.Status != StatusStarting {
		t.Fatalf("reopened session = %+v", got)
	}
}

func TestDeleteStartingRemovesOnlyProvisionalRecords(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_provisional", ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_main",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}

	if err := st.DeleteStarting(ctx, "ses_provisional"); err != nil {
		t.Fatalf("DeleteStarting: %v", err)
	}
	if _, err := st.Get(ctx, "ses_provisional"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get after DeleteStarting = %v, want ErrSessionNotFound", err)
	}
}

func TestStartingOnlyOperationsDoNotResurrectSettledSessions(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID: "ses_race", ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_main",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if _, err := st.PromoteToLive(ctx, "ses_race"); err != nil {
		t.Fatalf("PromoteToLive: %v", err)
	}
	if _, err := st.MarkEnded(ctx, "ses_race"); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}

	// A session settled by a concurrent End or Delete must not be
	// resurrected by the in-flight Start's transitions.
	if _, err := st.PromoteToLive(ctx, "ses_race"); !errors.Is(err, ErrSessionNotStarting) {
		t.Fatalf("PromoteToLive err = %v, want ErrSessionNotStarting", err)
	}
	if err := st.DeleteStarting(ctx, "ses_race"); !errors.Is(err, ErrSessionNotStarting) {
		t.Fatalf("DeleteStarting err = %v, want ErrSessionNotStarting", err)
	}
	if _, err := st.PromoteToLive(ctx, "ses_missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("PromoteToLive missing err = %v, want ErrSessionNotFound", err)
	}

	stored, err := st.Get(ctx, "ses_race")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != StatusEnded {
		t.Fatalf("status = %s, want ended", stored.Status)
	}
}

func TestListExcludesStartingAndFiltersPublicStatuses(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")
	clock := newTestClock(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC))
	st.now = clock.Now

	create := func(id string) {
		t.Helper()
		if _, err := st.CreateStarting(ctx, CreateSessionInput{
			ID: id, ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_main",
		}); err != nil {
			t.Fatalf("CreateStarting(%s): %v", id, err)
		}
	}

	create("ses_old")
	if _, err := st.PromoteToLive(ctx, "ses_old"); err != nil {
		t.Fatalf("PromoteToLive old: %v", err)
	}
	create("ses_middle")
	if _, err := st.PromoteToLive(ctx, "ses_middle"); err != nil {
		t.Fatalf("PromoteToLive middle: %v", err)
	}
	if _, err := st.MarkEnded(ctx, "ses_middle"); err != nil {
		t.Fatalf("MarkEnded middle: %v", err)
	}
	create("ses_provisional")

	defaultList, err := st.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List default: %v", err)
	}
	assertSessionIDs(t, defaultList, []string{"ses_middle", "ses_old"})

	live, err := st.List(ctx, ListFilter{Status: StatusLive})
	if err != nil {
		t.Fatalf("List Live: %v", err)
	}
	assertSessionIDs(t, live, []string{"ses_old"})

	ended, err := st.List(ctx, ListFilter{Status: StatusEnded})
	if err != nil {
		t.Fatalf("List ended: %v", err)
	}
	assertSessionIDs(t, ended, []string{"ses_middle"})

	all, err := st.ListAll(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	assertSessionIDs(t, all, []string{"ses_provisional", "ses_middle", "ses_old"})
}

func TestListOrdersSubSecondCreatesNewestFirst(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")
	// Timestamps 90ms apart share a whole second but differ in the fraction —
	// the case a variable-width timestamp format mis-sorts under a lexicographic
	// ORDER BY. Guards the fixed-width timestampLayout.
	base := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	var n int
	st.now = func() time.Time {
		t := base.Add(time.Duration(n) * 90 * time.Millisecond)
		n++
		return t
	}
	for _, id := range []string{"ses_first", "ses_second", "ses_third"} {
		if _, err := st.CreateStarting(ctx, CreateSessionInput{ID: id, ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_main"}); err != nil {
			t.Fatalf("CreateStarting(%s): %v", id, err)
		}
		if _, err := st.PromoteToLive(ctx, id); err != nil {
			t.Fatalf("PromoteToLive(%s): %v", id, err)
		}
	}
	list, err := st.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertSessionIDs(t, list, []string{"ses_third", "ses_second", "ses_first"})
}

func TestListBreaksCreatedAtTiesByInsertionOrder(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")
	// A frozen clock gives every record an identical created_at, so ordering
	// must fall back to insertion order (rowid), newest first. Insertion order is
	// deliberately not alphabetical so this would fail under the old `id DESC`
	// tiebreaker (which sorts by the unrelated random id, not insertion).
	frozen := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return frozen }
	for _, id := range []string{"ses_c", "ses_a", "ses_b"} {
		if _, err := st.CreateStarting(ctx, CreateSessionInput{ID: id, ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_main"}); err != nil {
			t.Fatalf("CreateStarting(%s): %v", id, err)
		}
		if _, err := st.PromoteToLive(ctx, id); err != nil {
			t.Fatalf("PromoteToLive(%s): %v", id, err)
		}
	}
	list, err := st.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertSessionIDs(t, list, []string{"ses_b", "ses_a", "ses_c"})
}

func TestWithTxCommitsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")

	if err := st.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.CreateStarting(ctx, CreateSessionInput{
			ID: "ses_committed", ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_main",
		}); err != nil {
			return err
		}
		_, err := tx.PromoteToLive(ctx, "ses_committed")
		return err
	}); err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}

	committed, err := st.Get(ctx, "ses_committed")
	if err != nil {
		t.Fatalf("Get committed: %v", err)
	}
	if committed.Status != StatusLive {
		t.Fatalf("committed status = %s, want %s", committed.Status, StatusLive)
	}

	rollbackErr := errors.New("rollback")
	err = st.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.CreateStarting(ctx, CreateSessionInput{
			ID: "ses_rolled_back", ActionID: "act_x", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: "wsp_main",
		}); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("WithTx rollback err = %v, want %v", err, rollbackErr)
	}
	if _, err := st.Get(ctx, "ses_rolled_back"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get rolled back err = %v, want ErrSessionNotFound", err)
	}
}

func TestInvalidInputsReturnClearErrors(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	seedWorkspace(t, st, "prj_main", "wsp_main")

	if _, err := st.List(ctx, ListFilter{Status: RecordStatus("bogus")}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("List invalid status err = %v, want ErrInvalidStatus", err)
	}

	if _, err := st.PromoteToLive(ctx, "ses_missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("PromoteToLive missing err = %v, want ErrSessionNotFound", err)
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
