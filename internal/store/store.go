// Package store is SQLite: the schema, the migrations, the repositories and the doctor.
//
// Two rules are enforced here rather than in the code above: nothing is ever hard-deleted, and history is
// append-only. Both are triggers in the schema, so a repository bug, a stray shell command or a future migration
// cannot break them quietly (docs/BUILD.md invariants 4 and 12).
//
// Concurrency follows docs/STANDARDS.md 2: one serialised writer that begins transactions IMMEDIATE, and a pool of
// read-only readers. A write transaction is never held across network I/O.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	// The pure-Go SQLite driver: no cgo, so the potato needs no C toolchain (docs/STANDARDS.md 2).
	_ "modernc.org/sqlite"

	"github.com/AutumnsGrove/sepia/internal/store/db"
)

// Options configure a Store. Clock and IDs are injected so a test can pin them (docs/STANDARDS.md 2).
type Options struct {
	// Path is the database file. Its directory is created if it is missing.
	Path string
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
	// NewID returns a new identifier. Nil means a random one.
	NewID func() string
}

// Store owns the database handles. Close it when the process stops.
type Store struct {
	path    string
	writer  *sql.DB
	readers *sql.DB
	now     func() time.Time
	newID   func() string
}

// Open creates the file if needed, applies the migrations, and returns a ready store.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("store: no database path")
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o750); err != nil {
		return nil, fmt.Errorf("store: create directory: %w", err)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = randomID
	}

	writer, err := sql.Open("sqlite", dsn(opts.Path, false))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	// One writer, serialised. The pool is one connection on purpose: SQLite has one write lock, and a queue of
	// connections waiting on it is a slower way to get the same thing.
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)

	readers, err := sql.Open("sqlite", dsn(opts.Path, true))
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("store: open readers: %w", err)
	}
	readers.SetMaxOpenConns(4)
	readers.SetMaxIdleConns(4)

	s := &Store{path: opts.Path, writer: writer, readers: readers, now: opts.Now, newID: opts.NewID}
	if err := s.ping(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err := s.Migrate(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// dsn builds the connection string. The pragmas are per-connection, so they belong in the DSN.
func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Add("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	} else {
		// Every write transaction begins IMMEDIATE, so the write lock is taken at BEGIN rather than on the first
		// write, which is what stops two writers deadlocking by upgrading a read lock.
		q.Add("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

// ping proves both handles work before the caller believes it has a store.
func (s *Store) ping(ctx context.Context) error {
	if err := s.writer.PingContext(ctx); err != nil {
		return fmt.Errorf("store: writer: %w", err)
	}
	if err := s.readers.PingContext(ctx); err != nil {
		return fmt.Errorf("store: reader: %w", err)
	}
	// WAL is set per connection; confirm it took, because a database left in rollback mode serialises readers
	// against the writer and would make the pool pointless.
	var mode string
	if err := s.readers.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("store: read journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("store: journal mode is %q, want wal", mode)
	}
	return nil
}

// Close releases both handles.
func (s *Store) Close() error {
	var first error
	if s.readers != nil {
		if err := s.readers.Close(); err != nil {
			first = err
		}
	}
	if s.writer != nil {
		if err := s.writer.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Path is the database file.
func (s *Store) Path() string { return s.path }

// Now is the injected clock.
func (s *Store) Now() time.Time { return s.now() }

// NewID is the injected identifier source.
func (s *Store) NewID() string { return s.newID() }

// write runs fn inside one transaction on the single writer. The writer's DSN sets _txlock=immediate, so the
// write lock is taken at BEGIN. The rollback on error uses a context that cannot be cancelled, because a cancelled
// request must still release the writer rather than leave the connection stuck in a transaction.
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w (rollback: %w)", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// read runs a query on the reader pool.
func (s *Store) read(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.readers.QueryContext(ctx, query, args...)
}

// randomID is a 16-byte identifier in base 36 with a readable prefix. It is not a UUID because nothing here needs
// one, and it is not derived from the clock because two ids must never collide.
func randomID() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A store without identifiers is worse than a slow one, so fall back rather than fail.
		return fmt.Sprintf("id_%d", time.Now().UnixNano())
	}
	out := make([]byte, 3+len(b))
	copy(out, "id_")
	for i, v := range b {
		out[3+i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out)
}

// Queries returns the generated queries bound to the writer, for a caller that is already inside a write
// transaction it owns. Reads belong on ReadQueries.
func (s *Store) Queries() *db.Queries { return db.New(s.writer) }

// ReadQueries returns the generated queries bound to the reader pool.
func (s *Store) ReadQueries() *db.Queries { return db.New(s.readers) }

// InTx runs fn with queries bound to one write transaction, so a repository method can be composed with others
// atomically: the confirm tap and the revision it creates are one call.
func (s *Store) InTx(ctx context.Context, fn func(*db.Queries) error) error {
	return s.write(ctx, func(tx *sql.Tx) error { return fn(db.New(tx)) })
}
