package store

import (
	"context"
	"testing"

	"github.com/AutumnsGrove/cairn/internal/domain"
)

// The marker is read from the first line only, and an unknown one is a load error rather than a comment: a
// migration that silently did not get the mode it asked for is worse than one that refuses to run.
func TestParsePragma(t *testing.T) {
	off, body, err := parsePragma("-- pragma: foreign_keys=off\nSELECT 1;")
	if err != nil || !off || body != "SELECT 1;" {
		t.Fatalf("parse = %v, %q, %v", off, body, err)
	}
	off, body, err = parsePragma("SELECT 1;")
	if err != nil || off || body != "SELECT 1;" {
		t.Fatalf("no marker = %v, %q, %v", off, body, err)
	}
	if _, _, err := parsePragma("-- pragma: make_it_fast\nSELECT 1;"); err == nil {
		t.Fatal("an unknown pragma was accepted")
	}
	// A marker on any line but the first is a comment, not a directive.
	if off, _, err := parsePragma("-- a note\n-- pragma: foreign_keys=off\nSELECT 1;"); err != nil || off {
		t.Fatalf("a later marker was honoured: off=%v err=%v", off, err)
	}
}

// Enforcement must be back on when the rebuild finishes. Left off, the next writer could bypass every trigger that
// protects the review, which is worse than the migration failing.
func TestRebuildRestoresForeignKeys(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	var enabled int
	if err := s.writer.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatalf("read pragma: %v", err)
	}
	if enabled != 1 {
		t.Errorf("foreign_keys = %d after migrating, want 1", enabled)
	}
}

// The rebuild keeps every row, and keeps every rule: an unrated draft is storable, a rating of 21 is not, and the
// triggers that stop a delete or a backward status change are still there afterwards.
func TestTheRebuildKeepsRowsAndRules(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seed(t, s)

	// Rebuild again over live data, the way 0004 did, and check the copy.
	body, err := migrationsFS.ReadFile("migrations/0004_allow_unrated_drafts.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	off, sql, err := parsePragma(string(body))
	if err != nil || !off {
		t.Fatalf("parse 0004 = %v, %v", off, err)
	}
	version, err := s.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if applyErr := s.apply(ctx, migration{version: version + 1, name: "rebuild_again", sql: sql, foreignKeysOff: true}); applyErr != nil {
		t.Fatalf("apply rebuild: %v", applyErr)
	}

	entry, err := s.Entry(ctx, "e1")
	if err != nil {
		t.Fatalf("entry after the rebuild: %v", err)
	}
	if entry.Rating != 18 || entry.Body != "loved it" {
		t.Errorf("entry = %+v, want the rows copied", entry)
	}

	// 0 is a draft with no rating yet.
	if err := raw(t, s, `UPDATE entries SET rating_q = 0 WHERE id = 'e1'`); err != nil {
		t.Errorf("an unrated draft was refused: %v", err)
	}
	if err := raw(t, s, `UPDATE entries SET rating_q = 21 WHERE id = 'e1'`); err == nil {
		t.Error("a rating of 21 was stored")
	}
	if err := raw(t, s, `DELETE FROM entries WHERE id = 'e1'`); err == nil {
		t.Error("the delete trigger did not survive the rebuild")
	}
	if err := raw(t, s, `UPDATE entries SET status = 'draft' WHERE id = 'e1'`); err == nil {
		t.Error("the status trigger did not survive the rebuild")
	}

	// And the indexes the lists depend on are back.
	for _, name := range []string{"entries_library", "entries_work", "entries_drafts"} {
		var found string
		if err := s.writer.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&found); err != nil {
			t.Errorf("index %s is missing after the rebuild: %v", name, err)
		}
	}
}

// A rebuild that leaves a dangling reference is rolled back whole, and the version does not move.
func TestABadRebuildIsRefused(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seed(t, s)
	before, err := s.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}

	// With enforcement off this statement succeeds, and leaves e1 pointing at a work that is gone.
	bad := "-- pragma: foreign_keys=off\nDELETE FROM works WHERE id = 'w1';"
	off, sql, err := parsePragma(bad)
	if err != nil || !off {
		t.Fatalf("parse = %v, %v", off, err)
	}
	if applyErr := s.apply(ctx, migration{version: before + 1, name: "bad_rebuild", sql: sql, foreignKeysOff: true}); applyErr == nil {
		t.Fatal("a rebuild with a dangling reference was committed")
	}

	after, err := s.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if after != before {
		t.Errorf("version moved to %d after a refused rebuild", after)
	}
	if _, err := s.Work(ctx, "w1"); err != nil {
		t.Errorf("the refused rebuild still removed the work: %v", err)
	}
	var enabled int
	if err := s.writer.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
		t.Errorf("foreign_keys = %d after a refused rebuild, want 1", enabled)
	}
}

// A draft's rating can be absent, and the domain guard still refuses to confirm it: the schema now permits the
// state, and the rule that matters lives where it always did.
func TestAnUnratedDraftCannotBeConfirmed(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seed(t, s)
	if err := s.CreateDraft(ctx, domain.Entry{
		ID: "e-unrated", WorkID: "w1", Status: domain.StatusDraft,
		RatingScale: "quarter_stars_20", Body: "", BodyOrigin: domain.BodyTyped,
	}); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	drafts, err := s.Drafts(ctx, 10)
	if err != nil {
		t.Fatalf("Drafts: %v", err)
	}
	found := false
	for _, draft := range drafts {
		if draft.ID == "e-unrated" {
			found = true
			if draft.Rating != 0 {
				t.Errorf("draft rating = %d", draft.Rating)
			}
		}
	}
	if !found {
		t.Fatal("the unrated draft is not in the Drafts list")
	}
	if err := domain.ValidateForSave(domain.Entry{WorkID: "w1", Rating: 0, Body: "words", BodyOrigin: domain.BodyTyped}, "words"); err == nil {
		t.Error("the save guard accepted a draft with no rating")
	}
}
