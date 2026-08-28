package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pressly/goose/v3"
)

func TestProjectsRoundTrip(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	projects := s.Projects()

	records := []ProjectRecord{
		{ID: "proj-aaaaa", Name: "atc", Directory: "/home/x/atc", CreatedAt: at(0), UpdatedAt: at(0)},
		{ID: "proj-bbbbb", Name: "notes", Directory: "/home/x/notes", CreatedAt: at(1), UpdatedAt: at(1)},
	}
	for _, record := range records {
		if ok, err := projects.Insert(ctx, record); err != nil || !ok {
			t.Fatalf("Insert = %v, %v; want true", ok, err)
		}
	}

	got, err := projects.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(records, got); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}

	// Insertion is the ID collision check: a conflicting id inserts
	// nothing and reports false.
	ok, err := projects.Insert(ctx, ProjectRecord{
		ID: "proj-aaaaa", Name: "impostor", Directory: "/elsewhere", CreatedAt: at(9), UpdatedAt: at(9),
	})
	if err != nil || ok {
		t.Fatalf("Insert(id collision) = %v, %v; want false", ok, err)
	}

	// The directory UNIQUE constraint is the backstop behind the domain's
	// pre-check: a fresh id claiming a taken directory errors.
	if _, err := projects.Insert(ctx, ProjectRecord{
		ID: "proj-ccccc", Name: "dup", Directory: "/home/x/atc", CreatedAt: at(9), UpdatedAt: at(9),
	}); err == nil {
		t.Fatal("Insert(duplicate directory) succeeded, want a constraint error")
	}

	record, found, err := projects.Get(ctx, "proj-aaaaa")
	if err != nil || !found || record.Name != "atc" {
		t.Errorf("Get = %+v, %v, %v", record, found, err)
	}
	if _, found, err := projects.Get(ctx, "proj-zzzzz"); err != nil || found {
		t.Errorf("Get(absent) = %v, %v; want false", found, err)
	}

	// The rename returns the committed row in the same operation.
	renamed, ok, err := projects.UpdateName(ctx, "proj-aaaaa", "renamed", at(2))
	if err != nil || !ok || renamed.Name != "renamed" || !renamed.UpdatedAt.Equal(at(2)) {
		t.Fatalf("UpdateName = %+v, %v, %v", renamed, ok, err)
	}
	if _, ok, err := projects.UpdateName(ctx, "proj-zzzzz", "x", at(2)); err != nil || ok {
		t.Fatalf("UpdateName(absent) = %v, %v; want false", ok, err)
	}

	if ok, err := projects.Delete(ctx, "proj-bbbbb"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v; want true", ok, err)
	}
	if ok, err := projects.Delete(ctx, "proj-bbbbb"); err != nil || ok {
		t.Fatalf("second Delete = %v, %v; want false", ok, err)
	}
}

func TestTerminalsByProjectAndDeleteBackstop(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	insertProject(t, s, "proj-aaaaa", "/a")
	insertProject(t, s, "proj-bbbbb", "/b")
	terminals := s.Terminals()
	for i, terminal := range []TerminalRecord{
		{ID: "term-aaaaa", ProjectID: "proj-aaaaa", Name: "one", Directory: "/a"},
		{ID: "term-bbbbb", ProjectID: "proj-aaaaa", Name: "two", Directory: "/a"},
		{ID: "term-ccccc", ProjectID: "proj-bbbbb", Name: "three", Directory: "/b"},
	} {
		terminal.CreatedAt, terminal.UpdatedAt = at(i), at(i)
		if ok, err := terminals.Insert(ctx, terminal); err != nil || !ok {
			t.Fatalf("Insert = %v, %v; want true", ok, err)
		}
	}

	got, err := terminals.ListIDsByProject(ctx, "proj-aaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"term-aaaaa", "term-bbbbb"}, got); diff != "" {
		t.Errorf("ListIDsByProject (-want +got):\n%s", diff)
	}

	// A terminal can never reference a missing project, and the violation
	// is typed so the domain can answer with its own error.
	if _, err := terminals.Insert(ctx, TerminalRecord{
		ID: "term-ddddd", ProjectID: "proj-zzzzz", Name: "stray", Directory: "/",
		CreatedAt: at(9), UpdatedAt: at(9),
	}); !errors.Is(err, ErrForeignKeyViolation) {
		t.Errorf("Insert(unknown project) = %v, want ErrForeignKeyViolation", err)
	}
	// And a project with terminals can never be deleted — the backstop
	// behind the domain's refuse-when-non-empty check.
	if _, err := s.Projects().Delete(ctx, "proj-aaaaa"); !errors.Is(err, ErrForeignKeyViolation) {
		t.Errorf("Delete(project with terminals) = %v, want ErrForeignKeyViolation", err)
	}
}

// The ATC-256 migration is a breaking reset: pre-migration terminal rows
// are gone afterwards, and the new schema requires project_id.
func TestProjectsMigrationResetsTerminals(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atc.db")

	// A database at the pre-projects schema with one legacy terminal row.
	db, err := sql.Open("sqlite", "file:"+path+"?"+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO terminals (id, name, directory, created_at, updated_at) VALUES ('term-aaaaa', 'Shell', '/', '2026-08-27T12:00:00.000000000Z', '2026-08-27T12:00:00.000000000Z')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	records, err := s.Terminals().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("legacy terminal rows survived the reset: %+v", records)
	}
	if _, err := s.Terminals().Insert(ctx, TerminalRecord{
		ID: "term-bbbbb", Name: "Shell", Directory: "/", CreatedAt: at(0), UpdatedAt: at(0),
	}); err == nil {
		t.Error("Insert without a project succeeded; the schema must require project_id")
	}
}
