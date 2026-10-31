package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Migrations are embedded and applied in order. They are append-only and positional: a migration's number is the
// PRAGMA user_version it leaves behind, so a database knows exactly where it is and a binary knows whether it is
// too old to read it (docs/slices/S04-data-layer.md 1).
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrTooNew means the file was written by a newer build. Refusing is the safe answer: reading a schema the binary
// does not understand is how a migration becomes a data loss.
var ErrTooNew = errors.New("store: the database is newer than this binary")

// migration is one file and the version it produces.
type migration struct {
	version int
	name    string
	sql     string
}

// Migrate brings the database up to the latest embedded version, one transaction per file.
func (s *Store) Migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	current, err := s.userVersion(ctx)
	if err != nil {
		return err
	}
	latest := 0
	if len(migrations) > 0 {
		latest = migrations[len(migrations)-1].version
	}
	if current > latest {
		return fmt.Errorf("%w: file is at version %d, this binary knows %d", ErrTooNew, current, latest)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := s.apply(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// apply runs one migration and stamps its version in the same transaction, so a crash leaves the database either
// before the migration or after it, never halfway.
func (s *Store) apply(ctx context.Context, m migration) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		// PRAGMA does not take a bound parameter, and m.version is an int parsed from our own file name.
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(m.version)); err != nil {
			return fmt.Errorf("store: set user_version for %s: %w", m.name, err)
		}
		return nil
	})
}

// Version is the database's current schema version.
func (s *Store) Version(ctx context.Context) (int, error) { return s.userVersion(ctx) }

func (s *Store) userVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.writer.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read user_version: %w", err)
	}
	return v, nil
}

// loadMigrations reads and orders the embedded files. A file whose name is not NNNN_name.sql is a build failure,
// because a migration that is silently skipped is worse than one that does not compile.
func loadMigrations() ([]migration, error) { return loadMigrationsFrom(migrationsFS, "migrations") }

// loadMigrationsFrom is loadMigrations with the filesystem passed in, so the failure paths (a badly named file, two
// files claiming one version, no files at all) can be tested without editing the embedded set.
func loadMigrationsFrom(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		number, name, ok := strings.Cut(base, "_")
		if !ok {
			return nil, fmt.Errorf("store: migration %q must be named NNNN_name.sql", e.Name())
		}
		version, err := strconv.Atoi(number)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("store: migration %q has no numeric version", e.Name())
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("store: two migrations claim version %d", out[i].version)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("store: no migrations are embedded")
	}
	return out, nil
}
