package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/store"
)

// --- revisit and a revision -----------------------------------------------------------------------------

func TestRevisionMovesOntoTheSavedEntry(t *testing.T) {
	e, db, sess := draftSession(t)
	ctx := context.Background()

	// The draft becomes a saved entry first, so there is something to revise.
	draft, err := e.EditDraft(ctx, sess.ID, Edit{BaseVersion: 0, Set: patch(map[string]any{
		"rating": 15, "body": "First thoughts, which I want to change.",
	})})
	if err != nil {
		t.Fatalf("EditDraft: %v", err)
	}
	saved, _, _, err := e.Confirm(ctx, sess.ID, ConfirmRequest{BaseVersion: draft.Version})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	// A new session offers the choice and opens a working copy.
	next, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	question, err := e.OfferRevisit(ctx, next.ID, saved, "The Lantern Keeper")
	if err != nil {
		t.Fatalf("OfferRevisit: %v", err)
	}
	if question.Kind != "revisit_choice" {
		t.Errorf("question = %+v", question)
	}
	revision, err := e.StartRevision(ctx, next.ID, saved)
	if err != nil {
		t.Fatalf("StartRevision: %v", err)
	}
	if revision.Entry.ID == saved.ID || revision.Entry.Rating != saved.Rating {
		t.Fatalf("working copy = %+v, want a copy with its own id and the same fields", revision.Entry)
	}

	edited, err := e.EditDraft(ctx, next.ID, Edit{BaseVersion: revision.Version, Set: patch(map[string]any{
		"body": "Second thoughts, which are better.",
	})})
	if err != nil {
		t.Fatalf("EditDraft: %v", err)
	}
	entry, _, _, err := e.Confirm(ctx, next.ID, ConfirmRequest{BaseVersion: edited.Version, Revises: saved.ID})
	if err != nil {
		t.Fatalf("Confirm revision: %v", err)
	}
	if entry.ID != saved.ID {
		t.Errorf("entry id = %q, want the saved entry to keep its id", entry.ID)
	}
	if entry.Body != "Second thoughts, which are better." {
		t.Errorf("body = %q", entry.Body)
	}

	// The saved entry is one row with one revision, and the working copy is retired rather than left as a second
	// review of the same work.
	revisions, err := db.Revisions(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revisions) != 1 || revisions[0].Snapshot.Body != "First thoughts, which I want to change." {
		t.Errorf("revisions = %+v", revisions)
	}
	library, _, err := db.Library(ctx, domain.Cursor{}, 10)
	if err != nil {
		t.Fatalf("Library: %v", err)
	}
	for _, row := range library {
		if row.WorkID == "w1" && row.ID != saved.ID {
			t.Errorf("the working copy is in the library: %+v", row)
		}
	}
	copyRow, err := db.Entry(ctx, revision.Entry.ID)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if !copyRow.Deleted {
		t.Error("the working copy was not retired")
	}
}

func TestStartRevisionRefusesASecondCard(t *testing.T) {
	e, db, sess := draftSession(t)
	if _, err := e.StartRevision(context.Background(), sess.ID, domain.Entry{ID: "e-other", WorkID: "w1"}); err == nil {
		t.Fatal("a session with a card accepted a second one")
	}
	if _, err := db.Session(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
}

// --- rounds and the queue -------------------------------------------------------------------------------

func TestAskRoundStopsAtTheCap(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	for i := 0; i < MaxQuestionRounds; i++ {
		if _, err := e.AskRound(ctx, sess.ID, "question_form", map[string]any{"questions": []string{}}); err != nil {
			t.Fatalf("round %d: %v", i+1, err)
		}
	}
	if _, err := e.AskRound(ctx, sess.ID, "question_form", map[string]any{"questions": []string{}}); !errors.Is(err, ErrTooManyRounds) {
		t.Fatalf("round five = %v, want ErrTooManyRounds", err)
	}
	if rounds, err := e.Rounds(ctx, sess.ID); err != nil || rounds != MaxQuestionRounds {
		t.Errorf("rounds = %d, %v", rounds, err)
	}
}

func TestQueueIsOneDeep(t *testing.T) {
	q := NewQueue()
	if err := q.Enqueue("s1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := q.Enqueue("s1"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Enqueue = %v, want ErrBusy", err)
	}
	if !q.Queued("s1") {
		t.Error("Queued = false with a message waiting")
	}
	if !q.Take("s1") {
		t.Error("Take returned nothing")
	}
	if q.Queued("s1") {
		t.Error("the queue still holds a message after Take")
	}
	// Another session is unaffected.
	if err := q.Enqueue("s2"); err != nil {
		t.Errorf("Enqueue(s2): %v", err)
	}
}

// --- the restart route ----------------------------------------------------------------------------------

// A session killed mid-turn comes back with its draft, its open question and its log, and resumes from the cut.
// This is the guarantee the whole log exists for; everything else in it is detail.
func TestASessionSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "cairn.db")
	var next int
	id := func() string { next++; return "id-" + itoa(next) }
	open := func() (*store.Store, *Engine) {
		db, err := store.Open(ctx, store.Options{Path: path, NewID: id})
		if err != nil {
			t.Fatalf("store.Open: %v", err)
		}
		if err := db.Migrate(ctx); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		return db, New(Config{Store: db, NewID: id, ServerVersion: "test"})
	}

	db, engine := open()
	if err := db.SaveWork(ctx, domain.Work{ID: "w1", Medium: "book", Title: "The Lantern Keeper", Verified: true}); err != nil {
		t.Fatalf("SaveWork: %v", err)
	}
	sess := draftFor(t, engine, db, "w1")
	if _, err := engine.EditDraft(ctx, sess.ID, Edit{BaseVersion: 0, Set: patch(map[string]any{"rating": 16})}); err != nil {
		t.Fatalf("EditDraft: %v", err)
	}
	question, err := engine.AskRound(ctx, sess.ID, "question_form", map[string]any{"questions": []string{"why"}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	head, err := db.HeadSequence(ctx, sess.ID)
	if err != nil {
		t.Fatalf("HeadSequence: %v", err)
	}
	_ = db.Close()

	// The process dies here. A new one opens the same file.
	db2, engine2 := open()
	defer func() { _ = db2.Close() }()
	hello, err := engine2.Greet(ctx, sess.ID, 0)
	if err != nil {
		t.Fatalf("Greet after restart: %v", err)
	}
	if hello.Draft == nil || hello.Draft.Entry.Rating != 16 {
		t.Fatalf("draft after restart = %+v", hello.Draft)
	}
	if len(hello.Open) != 1 || hello.Open[0].ID != question.ID {
		t.Fatalf("open questions after restart = %+v", hello.Open)
	}
	if hello.HeadSeq != head {
		t.Errorf("head = %d, want %d", hello.HeadSeq, head)
	}
	// Resuming from the middle replays the rest, in order, with no gap.
	replay, err := engine2.Greet(ctx, sess.ID, head-1)
	if err != nil {
		t.Fatalf("Greet from a cut: %v", err)
	}
	if len(replay.Events) != 1 || replay.Events[0].Seq != head {
		t.Errorf("replay = %+v", replay.Events)
	}
	// And the question can still be answered by the new process.
	if _, err := engine2.Answer(ctx, sess.ID, question.ID, map[string]any{"answers": []string{"because"}}, "m1"); err != nil {
		t.Fatalf("Answer after restart: %v", err)
	}
}

// itoa keeps the test's id source small.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
