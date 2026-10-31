package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// protectedTriggers must exist in every database. The doctor checks them by name, because a schema restored from
// a hand-edited backup is exactly where a trigger goes missing, and the invariants would then be enforced only by
// code that trusts the schema.
var protectedTriggers = []string{
	"entries_no_delete",
	"entry_revisions_no_delete",
	"entry_revisions_no_update",
	"work_sources_no_delete",
	"work_sources_only_accept",
	"answers_no_delete",
	"session_events_no_update",
	"entries_status_forward_only",
}

// Health is the doctor's answer. A store that is not healthy should not be used to save a review.
type Health struct {
	// Integrity is the result of PRAGMA integrity_check: "ok" when the file is sound.
	Integrity string
	// Version is the schema version the file reports.
	Version int
	// MissingTriggers lists the protections the file does not have.
	MissingTriggers []string
	// Permissions is the file mode, as a string for a log line.
	Permissions string
	// PermissionsOK is false when anyone but the owner can read the file.
	PermissionsOK bool
}

// OK reports whether the store is safe to use.
func (h Health) OK() bool {
	return h.Integrity == "ok" && len(h.MissingTriggers) == 0 && h.PermissionsOK
}

// Doctor checks the file, the schema's protections and the file's permissions. It is cheap enough to run at start
// and is what S13's health check and S14's restore verification call.
func (s *Store) Doctor(ctx context.Context) (Health, error) {
	var h Health

	if err := s.writer.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&h.Integrity); err != nil {
		return h, fmt.Errorf("store: integrity check: %w", err)
	}
	// integrity_check can return several rows when it finds problems; the first is the verdict.
	version, err := s.Version(ctx)
	if err != nil {
		return h, err
	}
	h.Version = version

	rows, err := s.read(ctx, `SELECT name FROM sqlite_master WHERE type = 'trigger'`)
	if err != nil {
		return h, fmt.Errorf("store: list triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	present := map[string]bool{}
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return h, fmt.Errorf("store: scan trigger: %w", scanErr)
		}
		present[name] = true
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return h, fmt.Errorf("store: list triggers: %w", rowsErr)
	}
	for _, name := range protectedTriggers {
		if !present[name] {
			h.MissingTriggers = append(h.MissingTriggers, name)
		}
	}
	sort.Strings(h.MissingTriggers)

	info, err := os.Stat(s.path)
	if err != nil {
		return h, fmt.Errorf("store: stat %s: %w", s.path, err)
	}
	h.Permissions = info.Mode().Perm().String()
	// The database holds the operator's own reviews. Group or other access is a bug, not a preference.
	h.PermissionsOK = info.Mode().Perm()&0o077 == 0

	return h, nil
}

// VerifyTriggers proves the protections are not merely present but working. Presence can be satisfied by a trigger
// that does nothing; this cannot. It writes probe rows inside one transaction, attempts every forbidden statement,
// and rolls the whole thing back with a sentinel error.
func (s *Store) VerifyTriggers(ctx context.Context) error {
	probes := []struct {
		name      string
		statement string
		fragment  string
	}{
		{"entries_no_delete", "DELETE FROM entries", "never deleted"},
		{"entry_revisions_no_delete", "DELETE FROM entry_revisions", "append-only"},
		{"entry_revisions_no_update", "UPDATE entry_revisions SET snapshot_json = '{}'", "append-only"},
		{"work_sources_no_delete", "DELETE FROM work_sources", "audit trail"},
		{"answers_no_delete", "DELETE FROM answers", "never deleted"},
		{"session_events_no_update", "UPDATE session_events SET kind = 'tampered'", "append-only log"},
		{"entries_status_forward_only", "UPDATE entries SET status = 'draft'", "never becomes a draft"},
		{"work_sources_only_accept", "UPDATE work_sources SET trust = 'verified'", "only be accepted"},
	}

	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := insertProbeRows(ctx, tx); err != nil {
			return err
		}
		for _, p := range probes {
			_, err := tx.ExecContext(ctx, p.statement)
			if err == nil {
				return fmt.Errorf("store: trigger %s did not fire", p.name)
			}
			if !strings.Contains(err.Error(), p.fragment) {
				return fmt.Errorf("store: trigger %s fired with an unexpected error: %w", p.name, err)
			}
		}
		// The probe rows must never survive: the sentinel rolls the whole transaction back.
		return errProbeRollback
	})
	if errors.Is(err, errProbeRollback) {
		return nil
	}
	return err
}

// errProbeRollback is how VerifyTriggers undoes its own probe rows.
var errProbeRollback = errors.New("store: roll back the trigger probe")

// insertProbeRows writes the minimum a forbidden statement needs a row to act on. Deleting from an empty table
// fires no trigger at all, so a probe without rows would pass whether or not the protection exists.
func insertProbeRows(ctx context.Context, tx *sql.Tx) error {
	const at = "2026-10-31T00:00:00Z"
	steps := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO works (id, medium, title, created_at, updated_at) VALUES ('probe-work', 'film', 'Probe', ?, ?)`, []any{at, at}},
		{`INSERT INTO work_sources (id, work_id, provider, trust, fetched_at) VALUES ('probe-source', 'probe-work', 'tmdb', 'unverified', ?)`, []any{at}},
		{`INSERT INTO entries (id, work_id, rating_q, body, body_origin, status, created_at, updated_at)
		  VALUES ('probe-entry', 'probe-work', 18, 'probe', 'typed', 'saved', ?, ?)`, []any{at, at}},
		{`INSERT INTO answers (id, entry_id, question_id, prompt, answer_type, created_at)
		  VALUES ('probe-answer', 'probe-entry', 'q', 'p', 'choice', ?)`, []any{at}},
		{`INSERT INTO entry_revisions (id, entry_id, revision, snapshot_json, created_at)
		  VALUES ('probe-revision', 'probe-entry', 1, '{}', ?)`, []any{at}},
		{`INSERT INTO sessions (id, kind, created_at, updated_at) VALUES ('probe-session', 'entry', ?, ?)`, []any{at, at}},
		{`INSERT INTO session_events (id, session_id, seq, kind, created_at)
		  VALUES ('probe-event', 'probe-session', 1, 'hello', ?)`, []any{at}},
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, step.query, step.args...); err != nil {
			return fmt.Errorf("store: trigger probe setup: %w", err)
		}
	}
	return nil
}

// TriggerNames is the list the doctor requires, exported for a test that asserts the schema and the list agree.
func TriggerNames() []string {
	out := make([]string, len(protectedTriggers))
	copy(out, protectedTriggers)
	return out
}
