package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/store"
)

// The session operations the agent needs that S07 did not have: start a card without a saved entry, attach the work
// the lookup chose, and write an extracted body from the model's quotes. Each one is a draft operation, so it keeps
// the version, the snapshot event and the containment rule.

func TestStartDraftLinksACardAndSnapshotsIt(t *testing.T) {
	e, db := newEngine(t)
	ctx := context.Background()
	if err := db.SaveWork(ctx, domain.Work{ID: "w1", Medium: "book", Title: "The Lantern Keeper", Verified: true}); err != nil {
		t.Fatalf("SaveWork: %v", err)
	}
	sess, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}

	draft, err := e.StartDraft(ctx, sess.ID, domain.Entry{
		ID: "e1", WorkID: "w1", Status: domain.StatusDraft, Rating: 17, BodyOrigin: domain.BodyTyped,
	})
	if err != nil {
		t.Fatalf("StartDraft: %v", err)
	}
	if draft.Version != 0 || draft.Entry.ID != "e1" || draft.Entry.WorkID != "w1" {
		t.Errorf("draft = %+v", draft)
	}
	events, err := e.store.EventsSince(ctx, sess.ID, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(events) != 1 || events[0].Kind != store.EventDraftSnapshot {
		t.Fatalf("events = %+v", events)
	}
	if _, err := e.StartDraft(ctx, sess.ID, domain.Entry{ID: "e2", Status: domain.StatusDraft}); err == nil {
		t.Fatal("a session accepted a second card")
	}
}

func TestSetWorkAttachesTheChosenWork(t *testing.T) {
	e, db, sess := draftSession(t)
	ctx := context.Background()
	cardless, err := e.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	// A card always has a work; this is the manual one an outage starts from.
	if saveErr := db.SaveWork(ctx, domain.Work{ID: "w-manual", Medium: "book", Title: "Untitled"}); saveErr != nil {
		t.Fatalf("SaveWork: %v", saveErr)
	}
	draft, err := e.StartDraft(ctx, cardless.ID, domain.Entry{
		ID: "e-cardless", WorkID: "w-manual", Status: domain.StatusDraft,
	})
	if err != nil {
		t.Fatalf("StartDraft: %v", err)
	}

	got, err := e.SetWork(ctx, cardless.ID, draft.Version, "w1", "v2")
	if err != nil {
		t.Fatalf("SetWork: %v", err)
	}
	if got.Entry.WorkID != "w1" || got.Version != 1 {
		t.Errorf("draft = %+v", got)
	}
	work, err := db.Work(ctx, "w1")
	if err != nil || work.Title != "The Lantern Keeper" {
		t.Errorf("work = %+v, %v", work, err)
	}

	// A stale version changes nothing.
	if _, err := e.SetWork(ctx, cardless.ID, 0, "w1", ""); refusalCode(err) != CodeStaleVersion {
		t.Errorf("stale SetWork = %v", err)
	}
	// An unknown work is refused by name, not left to the foreign key.
	if _, err := e.SetWork(ctx, sess.ID, 0, "w-nope", ""); err == nil {
		t.Error("SetWork accepted an unknown work")
	}
}

// refusalCode reads the stable code from either refusal type the engine uses.
func refusalCode(err error) string {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	var guard *domain.GuardError
	if errors.As(err, &guard) {
		return guard.Code
	}
	return ""
}

func TestSetExtractedBodyKeepsOnlyHerWords(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	source := "finished dune last night. the world felt so lived in but the middle dragged a bit, honestly"

	draft, err := e.SetExtractedBody(ctx, sess.ID, 0, source, []string{
		"the world felt so lived in but the middle dragged a bit",
	}, "honestly", "")
	if err != nil {
		t.Fatalf("SetExtractedBody: %v", err)
	}
	entry := draft.Entry
	if entry.BodyOrigin != domain.BodyExtracted {
		t.Errorf("origin = %q", entry.BodyOrigin)
	}
	if entry.Body != "the world felt so lived in but the middle dragged a bit" {
		t.Errorf("body = %q", entry.Body)
	}
	if entry.Verdict != "honestly" {
		t.Errorf("verdict = %q", entry.Verdict)
	}
	if len(entry.BodySpans) != 1 {
		t.Errorf("spans = %+v", entry.BodySpans)
	}
	if !strings.Contains(source, entry.BodySpans[0].Text) {
		t.Errorf("span text %q is not in her message", entry.BodySpans[0].Text)
	}
}

func TestSetExtractedBodyDropsInventedText(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	source := "I finished it and liked it."

	// Bad: every quote is invented, so there is no body and nothing is written.
	before, err := e.Draft(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if _, bodyErr := e.SetExtractedBody(ctx, sess.ID, before.Version, source, []string{"a paragraph I never wrote"}, "", ""); refusalCode(bodyErr) != domain.CodeBodyNotYourWords {
		t.Fatalf("invented body = %v", bodyErr)
	}
	after, err := e.Draft(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if after.Version != before.Version || after.Entry.Body != "" {
		t.Errorf("a refused body changed the card: %+v", after)
	}

	// Sad: an invented verdict is dropped while her body is kept.
	draft, err := e.SetExtractedBody(ctx, sess.ID, after.Version, source, []string{"liked it"}, "a verdict I never wrote", "")
	if err != nil {
		t.Fatalf("SetExtractedBody: %v", err)
	}
	if draft.Entry.Verdict != "" {
		t.Errorf("verdict = %q, want empty", draft.Entry.Verdict)
	}
	if draft.Entry.Body != "liked it" {
		t.Errorf("body = %q", draft.Entry.Body)
	}
}

func TestSetExtractedBodyRefusesAStaleVersion(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	if _, err := e.SetExtractedBody(ctx, sess.ID, 7, "I liked it.", []string{"liked it"}, "", ""); refusalCode(err) != CodeStaleVersion {
		t.Errorf("stale body = %v", err)
	}
}

func TestSourceTextJoinsHerMessagesInOrder(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	for i, text := range []string{"first thought", "second thought"} {
		if _, err := e.AppendUserMessage(ctx, sess.ID, text, "m"+string(rune('a'+i))); err != nil {
			t.Fatalf("AppendUserMessage: %v", err)
		}
	}
	got, err := e.SourceText(ctx, sess.ID)
	if err != nil {
		t.Fatalf("SourceText: %v", err)
	}
	if got != "first thought\nsecond thought" {
		t.Errorf("source = %q", got)
	}
	messages, err := e.UserMessages(ctx, sess.ID)
	if err != nil {
		t.Fatalf("UserMessages: %v", err)
	}
	if len(messages) != 2 || messages[0] != "first thought" || messages[1] != "second thought" {
		t.Errorf("messages = %v", messages)
	}
}
