package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
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

	// The update returns the committed row in the same operation.
	updated, ok, err := projects.Update(ctx, "proj-aaaaa", "renamed", "/moved", at(2))
	if err != nil || !ok || updated.Name != "renamed" || updated.Directory != "/moved" || !updated.UpdatedAt.Equal(at(2)) {
		t.Fatalf("Update = %+v, %v, %v", updated, ok, err)
	}
	if _, ok, err := projects.Update(ctx, "proj-zzzzz", "x", "/x", at(2)); err != nil || ok {
		t.Fatalf("Update(absent) = %v, %v; want false", ok, err)
	}

	if ok, err := projects.Delete(ctx, "proj-bbbbb"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v; want true", ok, err)
	}
	if ok, err := projects.Delete(ctx, "proj-bbbbb"); err != nil || ok {
		t.Fatalf("second Delete = %v, %v; want false", ok, err)
	}
}

// Spaces own terminals (ATC-296): a terminal can never reference a
// missing space, a space with terminals can never be deleted, and the
// violations are typed so the domain can answer with its own errors.
// Projects own nothing: one with threads deletes freely, and the threads
// survive unassigned.
func TestSpacesOwnTerminalsAndProjectsDoNot(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	insertSpace(t, s, "spce-aaaaa", "/a")
	insertProject(t, s, "proj-aaaaa", "/a")
	terminals := s.Terminals()
	if ok, err := s.Threads().InsertObserved(ctx, ThreadRecord{
		ID: "thrd-aaaaa", IntegrationID: "claude", AgentID: "claude", ProjectID: "proj-aaaaa", Status: "unknown",
		CreatedAt: at(0), UpdatedAt: at(0),
	}, ThreadIdentity{IntegrationID: "claude", ProviderConversationID: "sess-a", ThreadID: "thrd-aaaaa"}); err != nil || !ok {
		t.Fatalf("InsertObserved = %v, %v; want true", ok, err)
	}
	if ok, err := terminals.Insert(ctx, TerminalRecord{
		ID: "term-aaaaa", SpaceID: "spce-aaaaa", Name: "one", Directory: "/a", CreatedAt: at(0), UpdatedAt: at(0),
	}); err != nil || !ok {
		t.Fatalf("Insert = %v, %v; want true", ok, err)
	}
	if _, err := terminals.Insert(ctx, TerminalRecord{
		ID: "term-ddddd", SpaceID: "spce-zzzzz", Name: "stray", Directory: "/", CreatedAt: at(9), UpdatedAt: at(9),
	}); !errors.Is(err, ErrForeignKeyViolation) {
		t.Errorf("Insert(unknown space) = %v, want ErrForeignKeyViolation", err)
	}
	if _, err := s.Spaces().Delete(ctx, "spce-aaaaa"); !errors.Is(err, ErrForeignKeyViolation) {
		t.Errorf("Delete(space with terminals) = %v, want ErrForeignKeyViolation", err)
	}
	if ok, err := s.Projects().Delete(ctx, "proj-aaaaa"); err != nil || !ok {
		t.Errorf("Delete(project) = %v, %v; want true — projects own no terminals", ok, err)
	}
	if got, err := s.Threads().List(ctx); err != nil || len(got) != 1 || got[0].ProjectID != "" {
		t.Errorf("threads after project delete = %+v, %v; want the thread kept, unassigned", got, err)
	}

	// Exactly one Default space: a second is refused, typed.
	if _, err := s.Spaces().Insert(ctx, SpaceRecord{ID: "spce-defl1", Name: "Default", Directory: "/", IsDefault: true, CreatedAt: at(0), UpdatedAt: at(0)}); err != nil {
		t.Fatalf("Insert(default) = %v", err)
	}
	if _, err := s.Spaces().Insert(ctx, SpaceRecord{ID: "spce-defl2", Name: "Default", Directory: "/", IsDefault: true, CreatedAt: at(0), UpdatedAt: at(0)}); !errors.Is(err, ErrDefaultExists) {
		t.Errorf("Insert(second default) = %v, want ErrDefaultExists", err)
	}

	// Space records round-trip, update in one operation, and delete once
	// empty.
	updated, ok, err := s.Spaces().Update(ctx, "spce-aaaaa", "renamed", "/b", at(2))
	if err != nil || !ok || updated.Name != "renamed" || updated.Directory != "/b" || updated.IsDefault {
		t.Fatalf("Update = %+v, %v, %v", updated, ok, err)
	}
	if _, ok, err := s.Spaces().Update(ctx, "spce-zzzzz", "x", "/x", at(2)); err != nil || ok {
		t.Fatalf("Update(absent) = %v, %v; want false", ok, err)
	}
	list, err := s.Spaces().List(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "spce-aaaaa" || list[0].Name != "renamed" || !list[1].IsDefault {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if ok, err := terminals.Delete(ctx, "term-aaaaa"); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := s.Spaces().Delete(ctx, "spce-aaaaa"); err != nil || !ok {
		t.Errorf("Delete(empty space) = %v, %v; want true", ok, err)
	}
}

// The terminal migrations are breaking resets: pre-migration terminal
// rows are gone afterwards, and the current schema requires space_id.
func TestMigrationsResetTerminals(t *testing.T) {
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
		t.Error("Insert without a space succeeded; the schema must require space_id")
	}
}

// Migration 9 recreates terminals under spaces: a terminal-linked thread
// at version 8 survives the reset unlinked (the implicit delete fires ON
// DELETE SET NULL), its identity mapping with it.
func TestSpacesMigrationUnlinksThreads(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "atc.db")
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
	if _, err := provider.UpTo(ctx, 8); err != nil {
		t.Fatal(err)
	}
	stamp := at(0).UTC().Format(TimeFormat)
	for _, statement := range []string{
		`INSERT INTO projects (id, name, directory, created_at, updated_at) VALUES ('proj-aaaaa', 'p', '/', ?, ?)`,
		`INSERT INTO terminals (id, project_id, name, directory, created_at, updated_at) VALUES ('term-aaaaa', 'proj-aaaaa', 't', '/', ?, ?)`,
		`INSERT INTO threads (id, integration_id, project_id, terminal_id, status, created_at, updated_at) VALUES ('thrd-aaaaa', 'claude', 'proj-aaaaa', 'term-aaaaa', 'idle', ?, ?)`,
		`INSERT INTO thread_identities (integration_id, provider_conversation_id, thread_id) VALUES ('claude', 'sess-1', 'thrd-aaaaa')`,
	} {
		args := []any{stamp, stamp}
		if !strings.Contains(statement, "?") {
			args = nil
		}
		if _, err := db.ExecContext(ctx, statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	terminals, err := s.Terminals().List(ctx)
	if err != nil || len(terminals) != 0 {
		t.Errorf("terminals after migration = %+v, %v; want none", terminals, err)
	}
	threads, err := s.Threads().List(ctx)
	if err != nil || len(threads) != 1 || threads[0].TerminalID != nil || threads[0].ProjectID != "proj-aaaaa" {
		t.Errorf("threads after migration = %+v, %v; want the one thread unlinked from its terminal", threads, err)
	}
	if identities, err := s.Threads().ListIdentities(ctx); err != nil || len(identities) != 1 {
		t.Errorf("identities after migration = %+v, %v; want kept", identities, err)
	}
}
