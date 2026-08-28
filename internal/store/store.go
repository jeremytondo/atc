// Package store is the ATC-262 storage stack: one embedded SQLite file
// opened with the decided pragmas, forward-only goose migrations compiled
// into the binary, and typed repositories over sqlc-generated queries.
// Nothing outside a repository speaks SQL.
//
// Pool discipline for the database/sql-vs-single-writer trap: a write pool
// capped at one connection taking immediate transactions, plus a small
// read pool. Repositories route every statement through the right pool.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/jeremytondo/atc/internal/store/gen"
)

//go:embed migrations/*.sql
var migrations embed.FS

// TimeFormat is the fixed-width RFC 3339 UTC layout every timestamp column
// uses. Fixed width (unlike RFC3339Nano, which trims zeros) keeps lexical
// order equal to time order, which ListTerminals' ORDER BY relies on.
const TimeFormat = "2006-01-02T15:04:05.000000000Z"

// pragmas apply per connection via the DSN, so every pool member gets them.
const pragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"

// Store owns the two connection pools. Repositories hang off it; Close
// releases both pools.
type Store struct {
	reads  *sql.DB
	writes *sql.DB
}

// Open opens (creating if needed) the database at path and brings its
// schema current. Before applying pending migrations it copies the
// database file aside (path + ".backup"), so a botched upgrade has a
// manual recovery path. A database that cannot open or migrate fails
// closed with the error.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	if err := migrate(ctx, path); err != nil {
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}

	dsn := "file:" + path + "?" + pragmas
	writes, err := sql.Open("sqlite", dsn+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	// One writer connection is the whole write pool: SQLite has a single
	// writer, and database/sql handing writes a second connection would
	// trade the busy_timeout queue for immediate SQLITE_BUSY failures.
	writes.SetMaxOpenConns(1)

	reads, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = writes.Close()
		return nil, err
	}
	reads.SetMaxOpenConns(4)

	return &Store{reads: reads, writes: writes}, nil
}

// migrate runs pending migrations on a dedicated single connection,
// backing up the database file first. The migration connection is separate
// from the pools so the backup is taken before anything else touches the
// file in this process.
func migrate(ctx context.Context, path string) error {
	// Opening the database creates the file, so whether there is anything
	// worth backing up must be decided first.
	_, statErr := os.Stat(path)
	fresh := errors.Is(statErr, fs.ErrNotExist)

	db, err := sql.Open("sqlite", "file:"+path+"?"+pragmas)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, fsys)
	if err != nil {
		return err
	}
	pending, err := provider.HasPending(ctx)
	if err != nil {
		return err
	}
	if !pending {
		return nil
	}
	if !fresh {
		if err := backup(path); err != nil {
			return fmt.Errorf("pre-migration backup: %w", err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		return err
	}
	return nil
}

// backup copies the database file (and its WAL sidecars, which hold
// not-yet-checkpointed writes) beside itself. A database that does not
// exist yet needs no backup. Each migration run overwrites the previous
// backup: the recovery path is for the upgrade that just ran.
func backup(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := path + suffix
		destination := path + ".backup" + suffix
		if err := copyFile(source, destination); errors.Is(err, fs.ErrNotExist) {
			// No database (first boot) or no sidecar is a normal state;
			// remove a stale copy so the backup set stays consistent.
			if err := os.Remove(destination); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Close releases both pools.
func (s *Store) Close() error {
	return errors.Join(s.writes.Close(), s.reads.Close())
}

// Terminals returns the terminals repository.
func (s *Store) Terminals() *Terminals {
	return &Terminals{reads: gen.New(s.reads), writes: gen.New(s.writes)}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(TimeFormat)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(TimeFormat, value)
}
