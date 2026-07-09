package store

import (
	"context"
	"encoding/json"
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
		t.Fatalf("existing parent dir mode = %#o, want 0755", got)
	}
}

func TestCreateStartingAndTransitionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	clock := newTestClock(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC))
	st.now = clock.Now

	created, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_alpha",
		Name:        "build",
		Action:      "codex",
		Environment: "host-login-shell",
		Params:      json.RawMessage(`{"model":"gpt-5","resume":true}`),
		WorkingDir:  "/work",
		Prompt:      "implement the plan",
	})
	if err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if created.Status != StatusStarting {
		t.Fatalf("status = %s, want %s", created.Status, StatusStarting)
	}
	if created.Action != "codex" || created.Environment != "host-login-shell" {
		t.Fatalf("action/environment = %q/%q", created.Action, created.Environment)
	}
	if string(created.Params) != `{"model":"gpt-5","resume":true}` {
		t.Fatalf("params = %s", created.Params)
	}
	if created.CreatedAt.IsZero() || !created.CreatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("timestamps = created %s updated %s, want equal non-zero", created.CreatedAt, created.UpdatedAt)
	}

	running, err := st.MarkRunning(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if running.Status != StatusRunning {
		t.Fatalf("status = %s, want %s", running.Status, StatusRunning)
	}
	if !running.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("updatedAt = %s, want after %s", running.UpdatedAt, created.UpdatedAt)
	}

	terminated, err := st.MarkTerminated(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkTerminated: %v", err)
	}
	if terminated.Status != StatusTerminated {
		t.Fatalf("status = %s, want %s", terminated.Status, StatusTerminated)
	}
	if terminated.TerminatedAt == nil {
		t.Fatal("terminatedAt is nil")
	}
	terminatedAgain, err := st.MarkTerminated(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkTerminated again: %v", err)
	}
	if !terminatedAgain.UpdatedAt.Equal(terminated.UpdatedAt) {
		t.Fatalf("second terminate updatedAt = %s, want preserved %s", terminatedAgain.UpdatedAt, terminated.UpdatedAt)
	}

	archived, err := st.MarkArchived(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkArchived: %v", err)
	}
	if archived.Status != StatusTerminated {
		t.Fatalf("archived status = %s, want preserved %s", archived.Status, StatusTerminated)
	}
	if archived.ArchivedAt == nil {
		t.Fatal("archivedAt is nil")
	}

	got, err := st.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, archived) {
		t.Fatalf("Get = %+v, want %+v", got, archived)
	}
}

func TestOpenReopensExistingDatabase(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state", "atc.db")
	st := openTestStoreAt(t, dbPath)
	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_persisted",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
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

func TestMarkFailedStoresReasonAndCode(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_failed",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}

	failed, err := st.MarkFailed(ctx, "ses_failed", "action failed to launch", "launch_failed")
	if err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if failed.Status != StatusFailed || failed.FailureReason != "action failed to launch" || failed.FailureCode != "launch_failed" {
		t.Fatalf("failed session = %+v", failed)
	}
}

func TestMarkRunningClearsFailureFields(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_race",
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if _, err := st.MarkFailed(ctx, "ses_race", "session startup did not complete", "launch_failed"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	running, err := st.MarkRunning(ctx, "ses_race")
	if err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if running.Status != StatusRunning || running.FailureReason != "" || running.FailureCode != "" {
		t.Fatalf("running session = %+v, want running with no failure fields", running)
	}
}

func TestListOrderingStatusFilterAndArchivedDefault(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
	clock := newTestClock(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC))
	st.now = clock.Now

	create := func(id string) {
		t.Helper()
		if _, err := st.CreateStarting(ctx, CreateSessionInput{
			ID:          id,
			Action:      "codex",
			Environment: "host-login-shell",
			WorkingDir:  "/work",
		}); err != nil {
			t.Fatalf("CreateStarting(%s): %v", id, err)
		}
	}

	create("ses_old")
	if _, err := st.MarkRunning(ctx, "ses_old"); err != nil {
		t.Fatalf("MarkRunning old: %v", err)
	}
	create("ses_middle")
	if _, err := st.MarkFailed(ctx, "ses_middle", "launch failed", "launch_failed"); err != nil {
		t.Fatalf("MarkFailed middle: %v", err)
	}
	create("ses_new_archived")
	if _, err := st.MarkRunning(ctx, "ses_new_archived"); err != nil {
		t.Fatalf("MarkRunning new: %v", err)
	}
	if _, err := st.MarkArchived(ctx, "ses_new_archived"); err != nil {
		t.Fatalf("MarkArchived new: %v", err)
	}

	defaultList, err := st.List(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("List default: %v", err)
	}
	assertSessionIDs(t, defaultList, []string{"ses_middle", "ses_old"})

	running, err := st.List(ctx, ListFilter{Status: StatusRunning})
	if err != nil {
		t.Fatalf("List running: %v", err)
	}
	assertSessionIDs(t, running, []string{"ses_old"})

	withArchived, err := st.List(ctx, ListFilter{IncludeArchived: true})
	if err != nil {
		t.Fatalf("List include archived: %v", err)
	}
	assertSessionIDs(t, withArchived, []string{"ses_new_archived", "ses_middle", "ses_old"})

	archivedRunning, err := st.List(ctx, ListFilter{IncludeArchived: true, Status: StatusRunning})
	if err != nil {
		t.Fatalf("List archived running: %v", err)
	}
	assertSessionIDs(t, archivedRunning, []string{"ses_new_archived", "ses_old"})
}

func TestListOrdersSubSecondCreatesNewestFirst(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	defer st.Close()
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
		if _, err := st.CreateStarting(ctx, CreateSessionInput{ID: id, Action: "codex", Environment: "host-login-shell", WorkingDir: "/work"}); err != nil {
			t.Fatalf("CreateStarting(%s): %v", id, err)
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
	// A frozen clock gives every record an identical created_at, so ordering
	// must fall back to insertion order (rowid), newest first. Insertion order is
	// deliberately not alphabetical so this would fail under the old `id DESC`
	// tiebreaker (which sorts by the unrelated random id, not insertion).
	frozen := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return frozen }
	for _, id := range []string{"ses_c", "ses_a", "ses_b"} {
		if _, err := st.CreateStarting(ctx, CreateSessionInput{ID: id, Action: "codex", Environment: "host-login-shell", WorkingDir: "/work"}); err != nil {
			t.Fatalf("CreateStarting(%s): %v", id, err)
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

	if err := st.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.CreateStarting(ctx, CreateSessionInput{
			ID:          "ses_committed",
			Action:      "codex",
			Environment: "host-login-shell",
			WorkingDir:  "/work",
		}); err != nil {
			return err
		}
		_, err := tx.MarkRunning(ctx, "ses_committed")
		return err
	}); err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}

	committed, err := st.Get(ctx, "ses_committed")
	if err != nil {
		t.Fatalf("Get committed: %v", err)
	}
	if committed.Status != StatusRunning {
		t.Fatalf("committed status = %s, want %s", committed.Status, StatusRunning)
	}

	rollbackErr := errors.New("rollback")
	err = st.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.CreateStarting(ctx, CreateSessionInput{
			ID:          "ses_rolled_back",
			Action:      "codex",
			Environment: "host-login-shell",
			WorkingDir:  "/work",
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

	if _, err := st.CreateStarting(ctx, CreateSessionInput{
		ID:          "ses_bad_params",
		Action:      "codex",
		Environment: "host-login-shell",
		Params:      json.RawMessage(`[]`),
		WorkingDir:  "/work",
	}); err == nil {
		t.Fatal("CreateStarting accepted non-object params")
	}

	if _, err := st.List(ctx, ListFilter{Status: Status("bogus")}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("List invalid status err = %v, want ErrInvalidStatus", err)
	}

	if _, err := st.MarkRunning(ctx, "ses_missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("MarkRunning missing err = %v, want ErrSessionNotFound", err)
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
