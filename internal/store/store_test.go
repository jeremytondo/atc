package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atc.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	return s, path
}

func at(second int) time.Time {
	return time.Date(2026, 8, 27, 12, 0, second, 0, time.UTC)
}

func TestTerminalsRoundTrip(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	terminals := s.Terminals()
	insertProject(t, s, "proj-aaaaa", "/home/x")

	records := []TerminalRecord{
		{ID: "term-aaaaa", ProjectID: "proj-aaaaa", Name: "Shell", Directory: "/home/x", CreatedAt: at(0), UpdatedAt: at(0)},
		{ID: "term-bbbbb", ProjectID: "proj-aaaaa", Name: "hx", Directory: "/home/x/proj", Command: "hx .", CreatedAt: at(1), UpdatedAt: at(1)},
		{ID: "term-ccccc", ProjectID: "proj-aaaaa", Name: "Claude Code", Directory: "/home/x", Command: "claude", Agent: "claude", CreatedAt: at(2), UpdatedAt: at(2)},
	}
	for _, record := range records {
		if ok, err := terminals.Insert(ctx, record); err != nil || !ok {
			t.Fatalf("Insert = %v, %v; want true", ok, err)
		}
	}

	got, err := terminals.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(records, got); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}

	// Insertion is the ID collision check: a conflicting id inserts
	// nothing and reports false, leaving the existing record untouched.
	ok, err := terminals.Insert(ctx, TerminalRecord{
		ID: "term-aaaaa", ProjectID: "proj-aaaaa", Name: "impostor", Directory: "/", CreatedAt: at(9), UpdatedAt: at(9),
	})
	if err != nil || ok {
		t.Fatalf("Insert(collision) = %v, %v; want false", ok, err)
	}
	got, err = terminals.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(records, got); diff != "" {
		t.Errorf("collision mutated existing records (-want +got):\n%s", diff)
	}
}

func TestTerminalsMutations(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	terminals := s.Terminals()
	insertProject(t, s, "proj-aaaaa", "/")

	record := TerminalRecord{ID: "term-aaaaa", ProjectID: "proj-aaaaa", Name: "Shell", Directory: "/", CreatedAt: at(0), UpdatedAt: at(0)}
	if ok, err := terminals.Insert(ctx, record); err != nil || !ok {
		t.Fatalf("Insert = %v, %v; want true", ok, err)
	}

	if ok, err := terminals.UpdateName(ctx, "term-aaaaa", "build watcher", at(1)); err != nil || !ok {
		t.Fatalf("UpdateName = %v, %v; want true", ok, err)
	}
	if ok, err := terminals.UpdateName(ctx, "term-zzzzz", "x", at(1)); err != nil || ok {
		t.Fatalf("UpdateName(absent) = %v, %v; want false", ok, err)
	}
	if ok, err := terminals.RecordStopIntent(ctx, "term-aaaaa", at(2)); err != nil || !ok {
		t.Fatalf("RecordStopIntent = %v, %v; want true", ok, err)
	}
	code := 3
	if err := terminals.RecordExit(ctx, "term-aaaaa", at(3), at(4), &code); err != nil {
		t.Fatal(err)
	}

	// updated_at carries the observation time, not the exit time.
	stopAt, exitAt := at(2), at(3)
	want := []TerminalRecord{{
		ID: "term-aaaaa", ProjectID: "proj-aaaaa", Name: "build watcher", Directory: "/",
		CreatedAt: at(0), UpdatedAt: at(4),
		StopRequestedAt: &stopAt, ExitedAt: &exitAt, ExitCode: &code,
	}}
	got, err := terminals.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("after mutations (-want +got):\n%s", diff)
	}

	// The first observation of an exit wins; later evidence never rewrites it.
	other := 9
	if err := terminals.RecordExit(ctx, "term-aaaaa", at(9), at(9), &other); err != nil {
		t.Fatal(err)
	}
	got, err = terminals.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("exit evidence rewritten (-want +got):\n%s", diff)
	}

	if ok, err := terminals.Delete(ctx, "term-aaaaa"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v; want true", ok, err)
	}
	if ok, err := terminals.Delete(ctx, "term-aaaaa"); err != nil || ok {
		t.Fatalf("second Delete = %v, %v; want false", ok, err)
	}
}

// Reopening the same file must not re-run migrations, and the first open of
// an existing database must leave a pre-migration backup only when there was
// something to migrate.
func TestOpenIsIdempotentAndBacksUpBeforeMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atc.db")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// First boot: no pre-existing database, so nothing to back up.
	if _, err := os.Stat(path + ".backup"); !os.IsNotExist(err) {
		t.Errorf("fresh database left a backup (stat err %v)", err)
	}
	if err := terminalsInsertOne(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	got, err := second.Terminals().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("reopened database lost data: %d records", len(got))
	}
}

func terminalsInsertOne(ctx context.Context, s *Store) error {
	if _, err := s.Projects().Insert(ctx, ProjectRecord{
		ID: "proj-aaaaa", Name: "root", Directory: "/", CreatedAt: at(0), UpdatedAt: at(0),
	}); err != nil {
		return err
	}
	_, err := s.Terminals().Insert(ctx, TerminalRecord{
		ID: "term-aaaaa", ProjectID: "proj-aaaaa", Name: "Shell", Directory: "/", CreatedAt: at(0), UpdatedAt: at(0),
	})
	return err
}

// insertProject plants an owning project — the schema requires every
// terminal to reference one.
func insertProject(t *testing.T, s *Store, id, directory string) {
	t.Helper()
	ok, err := s.Projects().Insert(context.Background(), ProjectRecord{
		ID: id, Name: "p", Directory: directory, CreatedAt: at(0), UpdatedAt: at(0),
	})
	if err != nil || !ok {
		t.Fatalf("insertProject = %v, %v", ok, err)
	}
}
