package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// The data layer's tests are about what the DATABASE refuses, not what the code refuses. Every protection is
// exercised with raw SQL that bypasses the repositories, because that is the case the triggers exist for
// (docs/slices/S04-data-layer.md, "Bad").

func fixedNow() time.Time { return time.Date(2026, time.October, 31, 12, 0, 0, 0, time.UTC) }

func openTest(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cairn.db")
	// The identifier source counts up, because several rows in one test must not collide on the primary key.
	var next int
	s, err := Open(context.Background(), Options{
		Path:  path,
		Now:   fixedNow,
		NewID: func() string { next++; return fmt.Sprintf("test-id-%d", next) },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// raw runs a statement on the writer outside the repositories, which is how a test proves the schema is doing the
// protecting rather than the Go code.
func raw(t *testing.T, s *Store, query string, args ...any) error {
	t.Helper()
	_, err := s.writer.ExecContext(context.Background(), query, args...)
	return err
}

// seed writes one work, one saved entry, one answer, one revision and one source, so a forbidden statement has
// something to act on. Deleting from an empty table fires no trigger at all.
func seed(t *testing.T, s *Store) {
	t.Helper()
	const at = "2026-10-31T00:00:00Z"
	stmts := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO works (id, medium, title, created_at, updated_at) VALUES ('w1', 'book', 'Dune', ?, ?)`, []any{at, at}},
		{`INSERT INTO work_sources (id, work_id, provider, trust, fetched_at) VALUES ('s1', 'w1', 'openlibrary', 'verified', ?)`, []any{at}},
		{`INSERT INTO work_sources (id, work_id, provider, trust, fetched_at) VALUES ('s2', 'w1', 'web', 'unverified', ?)`, []any{at}},
		{`INSERT INTO entries (id, work_id, rating_q, body, body_origin, status, created_at, updated_at)
		  VALUES ('e1', 'w1', 18, 'loved it', 'typed', 'saved', ?, ?)`, []any{at, at}},
		{`INSERT INTO answers (id, entry_id, question_id, prompt, answer_type, created_at)
		  VALUES ('a1', 'e1', 'setting', 'How was the world?', 'tags', ?)`, []any{at}},
		{`INSERT INTO entry_revisions (id, entry_id, revision, snapshot_json, created_at) VALUES ('r1', 'e1', 1, '{}', ?)`, []any{at}},
		{`INSERT INTO sessions (id, kind, created_at, updated_at) VALUES ('sess1', 'entry', ?, ?)`, []any{at, at}},
		{`INSERT INTO session_events (id, session_id, seq, kind, created_at) VALUES ('ev1', 'sess1', 1, 'hello', ?)`, []any{at}},
	}
	for _, stmt := range stmts {
		if err := raw(t, s, stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed %q: %v", stmt.query, err)
		}
	}
}

// --- happy ---------------------------------------------------------------------------------------------

func TestOpenMigratesToTheCurrentVersion(t *testing.T) {
	s := openTest(t)
	version, err := s.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if version != migrations[len(migrations)-1].version {
		t.Fatalf("version = %d, want the latest embedded %d", version, migrations[len(migrations)-1].version)
	}

	for _, table := range []string{
		"works", "work_sources", "entries", "answers", "sessions", "session_events",
		"entry_revisions", "decision_calls", "turn_calls", "tags", "entry_tags", "media", "mirrors",
	} {
		var name string
		err := s.readers.QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s is missing: %v", table, err)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 3; i++ {
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate %d: %v", i, err)
		}
	}
}

func TestForeignKeysAreOn(t *testing.T) {
	s := openTest(t)
	err := raw(t, s, `INSERT INTO entries (id, work_id, rating_q, body, body_origin, created_at, updated_at)
		VALUES ('e', 'no-such-work', 18, 'x', 'typed', 'x', 'x')`)
	if err == nil {
		t.Fatal("an entry with no work was accepted")
	}
}

func TestDoctorIsHealthy(t *testing.T) {
	s := openTest(t)
	if err := os.Chmod(s.Path(), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := s.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !h.OK() {
		t.Fatalf("a fresh store is unhealthy: %+v", h)
	}
	if h.Version < 1 {
		t.Errorf("version = %d", h.Version)
	}
}

func TestVerifyTriggersOnAFreshStore(t *testing.T) {
	s := openTest(t)
	if err := s.VerifyTriggers(context.Background()); err != nil {
		t.Fatalf("VerifyTriggers: %v", err)
	}
	// The probe rows must not have survived.
	var count int
	if err := s.readers.QueryRowContext(context.Background(), `SELECT count(*) FROM entries`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d entries survived the probe rollback", count)
	}
}

// --- bad: the database refuses, even with raw SQL -------------------------------------------------------

func TestRawDeleteIsRefusedOnEveryProtectedTable(t *testing.T) {
	cases := map[string]string{
		"entries":         "entries are never deleted",
		"entry_revisions": "append-only",
		"work_sources":    "audit trail",
		"answers":         "never deleted",
	}
	for table, fragment := range cases {
		t.Run(table, func(t *testing.T) {
			s := openTest(t)
			seed(t, s)
			err := raw(t, s, "DELETE FROM "+table)
			if err == nil {
				t.Fatalf("DELETE FROM %s succeeded", table)
			}
			if !strings.Contains(err.Error(), fragment) {
				t.Fatalf("error = %v, want it to name the rule", err)
			}
		})
	}
}

func TestRawUpdateOfARevisionIsRefused(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := raw(t, s, `UPDATE entry_revisions SET snapshot_json = '{"tampered":true}'`); err == nil {
		t.Fatal("a revision was updated")
	}
}

func TestRawUpdateOfAnEventIsRefused(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := raw(t, s, `UPDATE session_events SET kind = 'tampered'`); err == nil {
		t.Fatal("a session event was rewritten")
	}
}

// A source snapshot may only be accepted, exactly once. Everything else about it is history.
func TestSourceSnapshotOnlyAcceptsOnce(t *testing.T) {
	s := openTest(t)
	seed(t, s)

	// The one legitimate change: an unverified web field, accepted once.
	if acceptErr := raw(t, s, `UPDATE work_sources SET trust = 'accepted', accepted_at = '2026-10-31T12:00:00Z' WHERE id = 's2'`); acceptErr != nil {
		t.Fatalf("accepting a web field was refused: %v", acceptErr)
	}

	// A second accept is a change to an accepted row, so it aborts.
	if twiceErr := raw(t, s, `UPDATE work_sources SET accepted_at = '2026-11-01T12:00:00Z' WHERE id = 's2'`); twiceErr == nil {
		t.Fatal("a snapshot was accepted twice")
	}

	// So does any other edit.
	for _, q := range []string{
		`UPDATE work_sources SET trust = 'verified' WHERE id = 's2'`,
		`UPDATE work_sources SET raw_json = '{"changed":true}' WHERE id = 's2'`,
		`UPDATE work_sources SET provider_id = 'other' WHERE id = 's2'`,
		`UPDATE work_sources SET fetched_at = '2020-01-01T00:00:00Z' WHERE id = 's2'`,
	} {
		if err := raw(t, s, q); err == nil {
			t.Errorf("%q was allowed", q)
		}
	}
}

// A verified source cannot be "accepted": the tap only ever moves an unverified field.
func TestVerifiedSourceCannotBeAccepted(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := raw(t, s, `UPDATE work_sources SET trust = 'accepted', accepted_at = '2026-10-31T12:00:00Z' WHERE id = 's1'`); err == nil {
		t.Fatal("a verified source was accepted as if it were a web field")
	}
}

func TestSavedEntryNeverReturnsToDraft(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := raw(t, s, `UPDATE entries SET status = 'draft' WHERE id = 'e1'`); err == nil {
		t.Fatal("a saved entry went back to draft")
	}
}

// Soft delete is an update, and it is the only way an entry leaves the library.
func TestSoftDeleteWorks(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := raw(t, s, `UPDATE entries SET deleted_at = '2026-11-01T00:00:00Z' WHERE id = 'e1'`); err != nil {
		t.Fatalf("soft delete was refused: %v", err)
	}
	var deleted sql.NullString
	if err := s.readers.QueryRowContext(context.Background(), `SELECT deleted_at FROM entries WHERE id = 'e1'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if !deleted.Valid {
		t.Fatal("deleted_at was not set")
	}
}

// A title shaped like SQL is stored verbatim and never executed.
func TestInjectionShapedTitleIsStoredVerbatim(t *testing.T) {
	s := openTest(t)
	hostile := `Robert'); DROP TABLE entries;--`
	err := raw(t, s, `INSERT INTO works (id, medium, title, created_at, updated_at) VALUES ('w2', 'book', ?, 'x', 'x')`, hostile)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	var got string
	if err := s.readers.QueryRowContext(context.Background(), `SELECT title FROM works WHERE id = 'w2'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != hostile {
		t.Fatalf("title = %q, want it stored exactly", got)
	}
	// The table is still there.
	var count int
	if err := s.readers.QueryRowContext(context.Background(), `SELECT count(*) FROM entries`).Scan(&count); err != nil {
		t.Fatalf("entries is gone: %v", err)
	}
}

// --- sad -----------------------------------------------------------------------------------------------

// A database written by a newer build is refused, because reading a schema this binary does not understand is how
// a migration becomes data loss.
func TestNewerDatabaseRefusesToOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cairn.db")
	s, err := Open(context.Background(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if bumpErr := raw(t, s, "PRAGMA user_version = 99"); bumpErr != nil {
		t.Fatal(bumpErr)
	}
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	_, err = Open(context.Background(), Options{Path: path})
	if !errors.Is(err, ErrTooNew) {
		t.Fatalf("err = %v, want ErrTooNew", err)
	}
}

// Sad: a corrupt file is reported by the integrity check rather than being trusted.
func TestCorruptFileIsDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cairn.db")
	s, err := Open(context.Background(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	// Overwrite the header with rubbish, the way a bad copy or a dying disk would.
	if writeErr := os.WriteFile(path, []byte(strings.Repeat("not a database ", 200)), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, err = Open(context.Background(), Options{Path: path})
	if err == nil {
		t.Fatal("a corrupt file opened without an error")
	}
}

// Sad: a cancelled context must release the writer, not leave it holding a transaction.
func TestCancelledContextReleasesTheWriter(t *testing.T) {
	s := openTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.write(ctx, func(*sql.Tx) error { return ctx.Err() })

	// The writer still works afterwards.
	done := make(chan error, 1)
	go func() {
		done <- s.write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(context.Background(),
				`INSERT INTO tags (id, name) VALUES ('t1', 'science fiction')`)
			return err
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the writer stayed blocked: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the writer was left holding a transaction")
	}
}

// Sad: a second writer waits for the first and then succeeds, rather than failing with a lock error.
func TestSecondWriterWaitsThenSucceeds(t *testing.T) {
	s := openTest(t)
	var wg sync.WaitGroup
	started := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.write(context.Background(), func(tx *sql.Tx) error {
			close(started)
			time.Sleep(50 * time.Millisecond)
			_, err := tx.ExecContext(context.Background(), `INSERT INTO tags (id, name) VALUES ('t1', 'a')`)
			return err
		})
		if err != nil {
			t.Errorf("first writer: %v", err)
		}
	}()
	<-started

	err := s.write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO tags (id, name) VALUES ('t2', 'b')`)
		return err
	})
	wg.Wait()
	if err != nil {
		t.Fatalf("second writer: %v", err)
	}
}

// Sad: the schema itself refuses a rating outside 1 to 20, whatever the code above believes.
func TestSchemaRefusesAnInvalidRating(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	if err := raw(t, s, `UPDATE entries SET rating_q = 21 WHERE id = 'e1'`); err == nil {
		t.Fatal("a rating of 21 quarters was stored")
	}
	if err := raw(t, s, `UPDATE entries SET rating_q = -1 WHERE id = 'e1'`); err == nil {
		t.Fatal("a rating of -1 quarters was stored")
	}
	// 0 is the "not rated yet" value a draft needs, and only a draft: a saved entry always has 1 to 20.
	if err := raw(t, s, `UPDATE entries SET rating_q = 0 WHERE id = 'e1'`); err != nil {
		t.Fatalf("an unrated draft was refused: %v", err)
	}
}

// Sad: a sequence number is unique per session, so a replay cannot overwrite what was already sent.
func TestEventSequenceIsUnique(t *testing.T) {
	s := openTest(t)
	seed(t, s)
	err := raw(t, s, `INSERT INTO session_events (id, session_id, seq, kind, created_at) VALUES ('ev2', 'sess1', 1, 'again', 'x')`)
	if err == nil {
		t.Fatal("two events shared a sequence number")
	}
}

// --- sad: a crash mid-transaction ----------------------------------------------------------------

// TestCrashChild is the subprocess the crash test kills. It opens the store, begins a write, and dies with SIGKILL
// before committing, which is the worst case the database has to survive.
func TestCrashChild(t *testing.T) {
	path := os.Getenv("CAIRN_CRASH_DB")
	if path == "" {
		t.Skip("only runs as a subprocess")
	}
	s, err := Open(context.Background(), Options{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = s.write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(),
			`INSERT INTO works (id, medium, title, created_at, updated_at) VALUES ('crash', 'book', 'Half written', 'x', 'x')`); err != nil {
			return err
		}
		// Signal readiness, then die without committing.
		fmt.Println("half-written")
		os.Exit(137)
		return nil
	})
}

// Sad: a process killed mid-transaction leaves a database that still opens, still passes the integrity check, and
// has none of the half-written row.
func TestCrashMidTransactionLeavesTheDatabaseSound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cairn.db")

	// Create the schema in the parent first, so the child only has to write.
	s, err := Open(context.Background(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=TestCrashChild", "-test.v=false")
	cmd.Env = append(os.Environ(), "CAIRN_CRASH_DB="+path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the child exited cleanly; it was supposed to be killed. output: %s", out)
	}

	reopened, err := Open(context.Background(), Options{Path: path})
	if err != nil {
		t.Fatalf("the database did not open after a crash: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	health, err := reopened.Doctor(context.Background())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if health.Integrity != "ok" {
		t.Fatalf("integrity = %q after a crash", health.Integrity)
	}

	var count int
	if err := reopened.readers.QueryRowContext(context.Background(),
		`SELECT count(*) FROM works WHERE id = 'crash'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("the half-written row survived: %d", count)
	}
}

// The doctor's list of required triggers must match the schema exactly: a trigger added to the migration but not to
// the list would be unverified, and one in the list but not the schema would make every fresh store unhealthy.
func TestDoctorTriggerListMatchesTheSchema(t *testing.T) {
	s := openTest(t)
	rows, err := s.readers.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type = 'trigger'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			t.Fatal(scanErr)
		}
		present[name] = true
	}
	for _, name := range TriggerNames() {
		if !present[name] {
			t.Errorf("the doctor requires %s, which the schema does not define", name)
		}
	}
	for name := range present {
		found := false
		for _, required := range TriggerNames() {
			if required == name {
				found = true
			}
		}
		if !found {
			t.Errorf("the schema defines %s, which the doctor does not check", name)
		}
	}
}

func TestInjectedClockAndIDs(t *testing.T) {
	s := openTest(t)
	if got := s.Now(); !got.Equal(fixedNow()) {
		t.Errorf("Now = %v, want the injected clock", got)
	}
	if got := s.NewID(); !strings.HasPrefix(got, "test-id-") {
		t.Errorf("NewID = %q, want the injected source", got)
	}
	if s.Queries() == nil {
		t.Error("Queries returned nil")
	}
	if s.ReadQueries() == nil {
		t.Error("ReadQueries returned nil")
	}
}

func TestRandomIDsDoNotRepeat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := randomID()
		if len(id) < 4 || !strings.HasPrefix(id, "id_") {
			t.Fatalf("id = %q", id)
		}
		if seen[id] {
			t.Fatalf("id repeated: %q", id)
		}
		seen[id] = true
	}
}

func TestOpenRejectsAnEmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), Options{Path: "  "}); err == nil {
		t.Fatal("an empty path opened a store")
	}
}

// A missing trigger is a missing protection, and the doctor says which one.
func TestDoctorReportsAMissingTrigger(t *testing.T) {
	s := openTest(t)
	if err := raw(t, s, `DROP TRIGGER entries_no_delete`); err != nil {
		t.Fatal(err)
	}
	health, err := s.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.OK() {
		t.Fatal("a store with no delete protection was reported healthy")
	}
	found := false
	for _, name := range health.MissingTriggers {
		if name == "entries_no_delete" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing = %v, want entries_no_delete", health.MissingTriggers)
	}

	// The active check agrees: a store whose protection is gone must not be trusted.
	if err := s.VerifyTriggers(context.Background()); err == nil {
		t.Fatal("VerifyTriggers passed with a trigger missing")
	}
}

// The database holds her own reviews, so group or other access is a bug.
func TestDoctorReportsLoosePermissions(t *testing.T) {
	s := openTest(t)
	if err := os.Chmod(s.Path(), 0o644); err != nil {
		t.Fatal(err)
	}
	health, err := s.Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.PermissionsOK {
		t.Fatalf("mode %s was reported as safe", health.Permissions)
	}
	if health.OK() {
		t.Fatal("a world-readable database was reported healthy")
	}
}

func TestLoadMigrationsRejectsBadNames(t *testing.T) {
	cases := map[string]map[string]string{
		"no number": {"init.sql": "SELECT 1;"},
		"no name":   {"0001.sql": "SELECT 1;"},
		"duplicate": {"0001_a.sql": "SELECT 1;", "0001_b.sql": "SELECT 2;"},
		"empty dir": {},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for file, body := range files {
				fsys["migrations/"+file] = &fstest.MapFile{Data: []byte(body)}
			}
			if _, err := loadMigrationsFrom(fsys, "migrations"); err == nil {
				t.Fatal("a bad migration set was accepted")
			}
		})
	}
}

// A migration that fails leaves nothing behind: the schema change and the version stamp are one transaction, so a
// broken migration cannot leave a database that claims to be newer than it is.
func TestAFailedMigrationChangesNothing(t *testing.T) {
	s := openTest(t)
	before, err := s.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = s.apply(context.Background(), migration{version: before + 1, name: "broken", sql: "THIS IS NOT SQL;"})
	if err == nil {
		t.Fatal("a broken migration was applied")
	}
	after, err := s.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("user_version moved from %d to %d after a failed migration", before, after)
	}
}
