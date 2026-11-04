package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/store"
)

// newEngine opens a store in a temporary directory and an engine over it with countable ids.
func newEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	ctx := context.Background()
	var next int
	var mu sync.Mutex
	db, err := store.Open(ctx, store.Options{
		Path: filepath.Join(t.TempDir(), "cairn.db"),
		NewID: func() string {
			mu.Lock()
			defer mu.Unlock()
			next++
			return fmt.Sprintf("id-%d", next)
		},
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return New(Config{Store: db, ServerVersion: "test"}), db
}

// --- lifecycle ------------------------------------------------------------------------------------------

func TestCreateAndClose(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()

	entry, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if entry.Kind != domain.SessionEntry || entry.State != domain.SessionActive || entry.DraftVersion != 0 {
		t.Errorf("session = %+v", entry)
	}

	general, err := e.CreateGeneral(ctx, "  what did I think of Dune?  ")
	if err != nil {
		t.Fatalf("CreateGeneral: %v", err)
	}
	if general.Kind != domain.SessionGeneral || general.Title != "what did I think of Dune?" {
		t.Errorf("general = %+v", general)
	}

	if err := e.Close(ctx, entry.ID, domain.SessionSaved); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := e.Close(ctx, entry.ID, domain.SessionDiscarded); !errors.Is(err, ErrClosed) {
		t.Errorf("second close = %v, want ErrClosed", err)
	}
	if err := e.Close(ctx, general.ID, domain.SessionActive); err == nil {
		t.Error("Close accepted an opening state")
	}
	// A closed session takes no new questions.
	if _, err := e.Ask(ctx, entry.ID, "describe", map[string]any{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Ask on a closed session = %v, want ErrClosed", err)
	}
}

// --- the log, hello and resume --------------------------------------------------------------------------

func TestAppendGivesStrictlyIncreasingSeq(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()
	sess, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	for i := 1; i <= 5; i++ {
		event, err := e.Append(ctx, sess.ID, "user_message", map[string]string{"text": fmt.Sprint(i)})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		if event.Seq != i {
			t.Fatalf("event %d has seq %d", i, event.Seq)
		}
	}
}

// Two goroutines appending at once must never pick the same number, and the log must stay gapless. This is the
// property the resume logic depends on.
func TestConcurrentAppendsKeepTheSequence(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()
	sess, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	const writers, each = 4, 8
	var wg sync.WaitGroup
	seen := make(chan int, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				event, appendErr := e.Append(ctx, sess.ID, "notice", map[string]string{"l": "info"})
				if appendErr != nil {
					t.Errorf("Append: %v", appendErr)
					return
				}
				seen <- event.Seq
			}
		}()
	}
	wg.Wait()
	close(seen)

	numbers := map[int]bool{}
	for seq := range seen {
		if numbers[seq] {
			t.Fatalf("seq %d was given twice", seq)
		}
		numbers[seq] = true
	}
	for i := 1; i <= writers*each; i++ {
		if !numbers[i] {
			t.Fatalf("seq %d is missing from a gapless log", i)
		}
	}
}

func TestGreetReplaysAndResumes(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()
	sess, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, appendErr := e.Append(ctx, sess.ID, "text_delta", map[string]string{"text": "x"}); appendErr != nil {
			t.Fatalf("Append: %v", appendErr)
		}
	}

	fresh, err := e.Greet(ctx, sess.ID, 0)
	if err != nil {
		t.Fatalf("Greet: %v", err)
	}
	if fresh.HeadSeq != 3 || len(fresh.Events) != 3 || fresh.FullState || fresh.ServerVersion != "test" {
		t.Errorf("fresh hello = %+v (a fresh connection replays the log)", fresh)
	}

	resumed, err := e.Greet(ctx, sess.ID, 1)
	if err != nil {
		t.Fatalf("Greet: %v", err)
	}
	if len(resumed.Events) != 2 || resumed.Events[0].Seq != 2 || resumed.Events[1].Seq != 3 {
		t.Errorf("replay = %+v", resumed.Events)
	}

	if _, err := e.Greet(ctx, sess.ID, 9); err == nil {
		t.Error("Greet accepted a last_seq past the head")
	}
}

// Compaction removes a finished turn's chunks. A client resuming across the gap cannot rebuild by replay, so hello
// says full_state and the client uses the snapshot instead (docs/PROTOCOL.md 6).
func TestCompactionForcesAFullState(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()
	sess, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if _, appendErr := e.Append(ctx, sess.ID, "user_message", map[string]string{"text": "hi"}); appendErr != nil {
		t.Fatal(appendErr)
	}
	for i := 0; i < 2; i++ {
		if _, appendErr := e.Append(ctx, sess.ID, "text_delta", map[string]string{"text": "chunk"}); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	if _, appendErr := e.Append(ctx, sess.ID, "assistant_end", map[string]string{"message_id": "m"}); appendErr != nil {
		t.Fatal(appendErr)
	}
	if compactErr := e.Compact(ctx, sess.ID); compactErr != nil {
		t.Fatalf("Compact: %v", err)
	}

	// The user message and the end survive; the two chunks do not.
	all, err := e.store.EventsSince(ctx, sess.ID, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	kinds := make([]string, 0, len(all))
	for _, event := range all {
		kinds = append(kinds, event.Kind)
	}
	if strings.Join(kinds, ",") != "user_message,assistant_end" {
		t.Fatalf("log after compaction = %v", kinds)
	}

	hello, err := e.Greet(ctx, sess.ID, 1)
	if err != nil {
		t.Fatalf("Greet: %v", err)
	}
	if !hello.FullState {
		t.Error("hello from inside a compacted gap did not ask for a full state")
	}
	if len(hello.Events) != 0 {
		t.Errorf("hello sent a partial replay across a gap: %+v", hello.Events)
	}
}

// --- interactions ---------------------------------------------------------------------------------------

func TestAskAndAnswer(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()
	sess, _ := e.CreateEntry(ctx)

	question, err := e.Ask(ctx, sess.ID, "choose_medium", map[string]any{"options": []string{"book", "film"}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if question.OpenedSeq != 1 || !question.Open() {
		t.Errorf("question = %+v", question)
	}

	answered, err := e.Answer(ctx, sess.ID, question.ID, map[string]string{"value": "book"}, "m1")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if answered.ResolvedSeq == nil || answered.ResolvedBy != domain.ResolvedByUser {
		t.Errorf("answered = %+v", answered)
	}
	if _, err := e.Answer(ctx, sess.ID, question.ID, map[string]string{"value": "film"}, "m2"); !errors.Is(err, store.ErrInteractionResolved) {
		t.Errorf("second answer = %v, want the first to win", err)
	}

	// Only the first tap is in the ledger, so a retry of the second has nothing to return.
	if _, seen, err := e.Seen(ctx, sess.ID, "m2"); err != nil || seen {
		t.Errorf("m2 recorded: seen=%v err=%v", seen, err)
	}
	if msg, seen, err := e.Seen(ctx, sess.ID, "m1"); err != nil || !seen || msg.Status != "accepted" {
		t.Errorf("m1 = %+v seen=%v err=%v", msg, seen, err)
	}
}

func TestWithdraw(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()
	sess, _ := e.CreateEntry(ctx)
	question, err := e.Ask(ctx, sess.ID, "lookup_failed", map[string]any{"service": "tmdb"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	withdrawn, err := e.Withdraw(ctx, sess.ID, question.ID)
	if err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	if withdrawn.ResolvedBy != domain.ResolvedByServer || withdrawn.Open() {
		t.Errorf("withdrawn = %+v", withdrawn)
	}
}

// --- idempotency ----------------------------------------------------------------------------------------

func TestSeenReturnsTheFirstOutcome(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()
	sess, _ := e.CreateEntry(ctx)
	if _, seen, err := e.Seen(ctx, sess.ID, "nope"); err != nil || seen {
		t.Fatalf("unknown id: seen=%v err=%v", seen, err)
	}
	first, err := e.Append(ctx, sess.ID, "user_message", map[string]string{"text": "hi"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if rememberErr := e.store.RememberClient(ctx, domain.ClientMessage{
		SessionID: sess.ID, ClientMsgID: "m1", Seq: first.Seq, Status: "accepted",
	}); rememberErr != nil {
		t.Fatalf("RememberClient: %v", rememberErr)
	}
	msg, seen, err := e.Seen(ctx, sess.ID, "m1")
	if err != nil || !seen || msg.Seq != first.Seq {
		t.Fatalf("Seen = %+v, %v, %v", msg, seen, err)
	}
}

// --- the drafts list ------------------------------------------------------------------------------------

// draftFor links a fresh draft entry to the session, so a test can walk the waiting reasons.
func draftFor(t *testing.T, e *Engine, db *store.Store, workID string) domain.Session {
	t.Helper()
	ctx := context.Background()
	sess, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	entry := domain.Entry{
		ID: "e-" + sess.ID, WorkID: workID, Status: domain.StatusDraft,
		RatingScale: "quarter_star", BodyOrigin: domain.BodyTyped,
	}
	if err := db.CreateDraft(ctx, entry); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if err := db.LinkDraft(ctx, sess.ID, entry.ID); err != nil {
		t.Fatalf("LinkDraft: %v", err)
	}
	return sess
}

func TestDraftsWaitingReasons(t *testing.T) {
	e, db := newEngine(t)
	ctx := context.Background()

	if err := db.SaveWork(ctx, domain.Work{ID: "w1", Medium: "book", Title: "The Lantern Keeper", Verified: true}); err != nil {
		t.Fatalf("SaveWork: %v", err)
	}
	// A session with no work yet.
	bare, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	drafts, err := e.Drafts(ctx, 10)
	if err != nil {
		t.Fatalf("Drafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Reason != WaitingPickWork {
		t.Fatalf("drafts = %+v", drafts)
	}

	// A draft with a work, no rating.
	sess := draftFor(t, e, db, "w1")
	drafts, err = e.Drafts(ctx, 10)
	if err != nil {
		t.Fatalf("Drafts: %v", err)
	}
	bySession := map[string]DraftSummary{}
	for _, summary := range drafts {
		bySession[summary.Session.ID] = summary
	}
	if got := bySession[sess.ID]; got.Reason != WaitingRating || got.WorkTitle != "The Lantern Keeper" {
		t.Errorf("summary = %+v", got)
	}
	if got := bySession[bare.ID]; got.Reason != WaitingPickWork {
		t.Errorf("bare summary = %+v", got)
	}
}

func TestWaitingReasonOrder(t *testing.T) {
	cases := []struct {
		name       string
		entry      domain.Entry
		unverified bool
		want       WaitingReason
	}{
		{"rating first", domain.Entry{}, false, WaitingRating},
		{"then the body", domain.Entry{Rating: 17}, false, WaitingBody},
		{"then the unverified field", domain.Entry{Rating: 17, Body: "words"}, true, WaitingFields},
		{"then the date", domain.Entry{Rating: 17, Body: "words"}, false, WaitingDate},
		{"then ready", domain.Entry{Rating: 17, Body: "words", FinishedOn: "2026-10-30"}, false, WaitingReady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := waitingReason(tc.entry, tc.unverified); got != tc.want {
				t.Errorf("waitingReason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGreetIncludesTheDraftAndOpenQuestions(t *testing.T) {
	e, db := newEngine(t)
	ctx := context.Background()
	if err := db.SaveWork(ctx, domain.Work{ID: "w1", Medium: "book", Title: "A Book", Verified: true}); err != nil {
		t.Fatalf("SaveWork: %v", err)
	}
	sess := draftFor(t, e, db, "w1")
	if _, err := e.Ask(ctx, sess.ID, "question_form", map[string]any{"questions": []string{}}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	hello, err := e.Greet(ctx, sess.ID, 0)
	if err != nil {
		t.Fatalf("Greet: %v", err)
	}
	if hello.Draft == nil || hello.Draft.Entry.ID != "e-"+sess.ID {
		t.Fatalf("hello draft = %+v", hello.Draft)
	}
	if len(hello.Draft.Open) != 1 || hello.Draft.Open[0].Kind != "question_form" {
		t.Errorf("draft questions = %+v", hello.Draft.Open)
	}
	if len(hello.Open) != 1 {
		t.Errorf("hello open = %+v", hello.Open)
	}
}

// --- payload encoding -----------------------------------------------------------------------------------

func TestAppendEncodesPayloads(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()
	sess, _ := e.CreateEntry(ctx)
	event, err := e.Append(ctx, sess.ID, "notice", map[string]string{"code": "pre_read_unavailable", "text": "no"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(event.Payload, &decoded); err != nil {
		t.Fatalf("payload = %s: %v", event.Payload, err)
	}
	if decoded["code"] != "pre_read_unavailable" {
		t.Errorf("payload = %s", event.Payload)
	}
}
