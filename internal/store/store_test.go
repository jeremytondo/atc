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

	records := []TerminalRecord{
		{ID: "term-aaaaa", Name: "Shell", Directory: "/home/x", CreatedAt: at(0), UpdatedAt: at(0)},
		{ID: "term-bbbbb", Name: "hx", Directory: "/home/x/proj", App: "hx .", CreatedAt: at(1), UpdatedAt: at(1)},
	}
	for _, record := range records {
		if err := terminals.Insert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}

	got, err := terminals.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(records, got); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}

	taken, err := terminals.IDTaken(ctx, "term-aaaaa")
	if err != nil || !taken {
		t.Errorf("IDTaken(existing) = %v, %v; want true", taken, err)
	}
	taken, err = terminals.IDTaken(ctx, "term-zzzzz")
	if err != nil || taken {
		t.Errorf("IDTaken(absent) = %v, %v; want false", taken, err)
	}
}

func TestTerminalsMutations(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	terminals := s.Terminals()

	record := TerminalRecord{ID: "term-aaaaa", Name: "Shell", Directory: "/", CreatedAt: at(0), UpdatedAt: at(0)}
	if err := terminals.Insert(ctx, record); err != nil {
		t.Fatal(err)
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
	if err := terminals.RecordExit(ctx, "term-aaaaa", at(3), &code); err != nil {
		t.Fatal(err)
	}

	stopAt, exitAt := at(2), at(3)
	want := []TerminalRecord{{
		ID: "term-aaaaa", Name: "build watcher", Directory: "/",
		CreatedAt: at(0), UpdatedAt: at(3),
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
	if err := terminals.RecordExit(ctx, "term-aaaaa", at(9), &other); err != nil {
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
	return s.Terminals().Insert(ctx, TerminalRecord{
		ID: "term-aaaaa", Name: "Shell", Directory: "/", CreatedAt: at(0), UpdatedAt: at(0),
	})
}
