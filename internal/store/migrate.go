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
	// foreignKeysOff is set by a `-- pragma: foreign_keys=off` first line. Widening a CHECK means rebuilding the
	// table, and SQLite's documented rebuild cannot run with enforcement on, so this file opts out and pays for it
	// with the checks in apply (S07 decisions, docs/BUILD-LOG.md).
	foreignKeysOff bool
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
	if !m.foreignKeysOff {
		return s.write(ctx, func(tx *sql.Tx) error { return s.applyInTx(ctx, tx, m) })
	}
	return s.applyRebuild(ctx, m)
}

// applyInTx runs a migration's statements and stamps its version, inside a transaction the caller owns.
func (s *Store) applyInTx(ctx context.Context, tx *sql.Tx, m migration) error {
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	// PRAGMA does not take a bound parameter, and m.version is an int parsed from our own file name.
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(m.version)); err != nil {
		return fmt.Errorf("store: set user_version for %s: %w", m.name, err)
	}
	return nil
}

// applyRebuild runs one table rebuild in SQLite's documented order: enforcement off on the connection, one
// transaction, and a commit only after the foreign keys and the file itself check out. Enforcement is turned back
// on whatever happens, including a cancellation, because a writer left with it off would let the invariants the
// triggers enforce be bypassed by the next statement.
func (s *Store) applyRebuild(ctx context.Context, m migration) error {
	conn, err := s.writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("store: migration %s: foreign keys off: %w", m.name, err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON")
	}()
	var enabled int
	if pragmaErr := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); pragmaErr != nil {
		return fmt.Errorf("store: migration %s: read foreign_keys: %w", m.name, pragmaErr)
	}
	if enabled != 0 {
		return fmt.Errorf("store: migration %s: foreign keys are still on, so SQLite would ignore the pragma", m.name)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: migration %s: begin: %w", m.name, err)
	}
	if applyErr := s.applyInTx(ctx, tx, m); applyErr != nil {
		_ = tx.Rollback()
		return applyErr
	}
	if checkErr := checkRebuilt(ctx, tx); checkErr != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: migration %s: %w", m.name, checkErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return fmt.Errorf("store: migration %s: commit: %w", m.name, commitErr)
	}
	return nil
}

// checkRebuilt refuses the commit when a rebuild left a dangling reference or an unsound file. Both checks run
// inside the transaction, so a failure rolls the rebuild back rather than leaving half a table.
func checkRebuilt(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		return errors.New("the rebuild left a foreign key violation")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	var integrity string
	if err := tx.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("the rebuild left the database unsound: %s", integrity)
	}
	return nil
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

// pragmaMarker is how a migration asks for a mode the runner cannot infer. It must be the first line.
const pragmaMarker = "-- pragma: "

// parsePragma reads the optional first-line directive and returns the SQL without it. An unknown directive is a
// build-time error, not a comment: a migration that silently did not get the mode it asked for is worse than one
// that refuses to load.
func parsePragma(sql string) (foreignKeysOff bool, body string, err error) {
	first, rest, _ := strings.Cut(sql, "\n")
	first = strings.TrimSpace(first)
	if !strings.HasPrefix(first, pragmaMarker) {
		return false, sql, nil
	}
	switch directive := strings.TrimSpace(strings.TrimPrefix(first, pragmaMarker)); directive {
	case "foreign_keys=off":
		return true, rest, nil
	default:
		return false, "", fmt.Errorf("unknown pragma %q; known pragmas are foreign_keys=off", directive)
	}
}

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
		foreignKeysOff, sql, err := parsePragma(string(body))
		if err != nil {
			return nil, fmt.Errorf("store: migration %q: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: name, sql: sql, foreignKeysOff: foreignKeysOff})
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
