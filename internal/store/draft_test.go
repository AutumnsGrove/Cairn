package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AutumnsGrove/sepia/internal/domain"
)

// The card's writes are the transactions S07 cannot split: the version check and the change it guards, the event
// and the row it describes, the ledger row and the effect it remembers. Each test below takes the happy route, the
// bad route (a change the database must refuse) and the sad route (a row that is not there), because a card that
// half-changed is exactly what these transactions exist to prevent.

// draftRig is an active entry session with one unrated draft linked to a work, which is what the engine holds while
// she edits the card.
func draftRig(t *testing.T) (*Store, domain.Session) {
	t.Helper()
	s := openTest(t)
	ctx := context.Background()
	if err := s.SaveWork(ctx, sampleWork("w1")); err != nil {
		t.Fatalf("SaveWork: %v", err)
	}
	if err := s.CreateSession(ctx, domain.Session{
		ID: "s1", Kind: domain.SessionEntry, State: domain.SessionActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.CreateDraft(ctx, sampleEntry("e1", "w1", 0)); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if err := s.LinkDraft(ctx, "s1", "e1"); err != nil {
		t.Fatalf("LinkDraft: %v", err)
	}
	sess, err := s.Session(ctx, "s1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	return s, sess
}

func kindsOf(t *testing.T, s *Store, sessionID string) []string {
	t.Helper()
	events, err := s.EventsSince(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

func TestUpdateDraftWritesTheChangeAndItsEventTogether(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	season := 2

	entry, version, event, err := s.UpdateDraft(ctx, UpdateDraft{
		SessionID: "s1", BaseVersion: 0, EntryID: "e1", ClientMsgID: "m1",
		Mutate: func(e *domain.Entry) {
			e.Rating = 18
			e.Season = &season
			e.Favourite = true
			e.Body = "the world felt so lived in"
			e.BodyOrigin = domain.BodyTyped
		},
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}
	if entry.Rating != 18 || !entry.Favourite || entry.Season == nil || *entry.Season != 2 {
		t.Errorf("entry = %+v", entry)
	}
	if event.Kind != EventDraftSnapshot || event.Seq != 1 {
		t.Fatalf("event = %+v", event)
	}
	// The event carries the card a client redraws from, so it is the snapshot, not the edit.
	var payload struct {
		Version int          `json:"version"`
		Draft   domain.Entry `json:"draft"`
	}
	if decodeErr := json.Unmarshal(event.Payload, &payload); decodeErr != nil {
		t.Fatalf("event payload: %v", decodeErr)
	}
	if payload.Version != 1 || payload.Draft.Body != "the world felt so lived in" {
		t.Errorf("event payload = %+v", payload)
	}

	stored, err := s.Entry(ctx, "e1")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if stored.Body != "the world felt so lived in" || stored.Status != domain.StatusDraft {
		t.Errorf("stored = %+v", stored)
	}
	sess, err := s.Session(ctx, "s1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.DraftVersion != 1 || sess.EntryID != "e1" {
		t.Errorf("session = %+v", sess)
	}
	if got := kindsOf(t, s, "s1"); len(got) != 1 || got[0] != EventDraftSnapshot {
		t.Errorf("events = %v", got)
	}
	// The client message is remembered in the same transaction, so a retry after a reconnect is answered once.
	msg, err := s.ClientMessage(ctx, "s1", "m1")
	if err != nil {
		t.Fatalf("ClientMessage: %v", err)
	}
	if msg.Seq != 1 || msg.Status != acceptedStatus {
		t.Errorf("client message = %+v", msg)
	}

	// A second edit names the version the first one left, and clearing the season covers the nil widening.
	edited, version, _, err := s.UpdateDraft(ctx, UpdateDraft{
		SessionID: "s1", BaseVersion: 1, EntryID: "e1", ClientMsgID: "m2",
		Mutate: func(e *domain.Entry) {
			e.Season = nil
			e.Verdict = "quiet"
		},
	})
	if err != nil {
		t.Fatalf("UpdateDraft (second): %v", err)
	}
	if version != 2 || edited.Season != nil || edited.Verdict != "quiet" {
		t.Errorf("second edit = %+v at version %d", edited, version)
	}
}

func TestUpdateDraftRefusesAStaleVersionAndChangesNothing(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	first := &seasonChange{rating: 18}
	mustUpdate(t, s, 0, first)

	// The same base version again is a stale edit: no row updated means no change at all.
	_, _, _, err := s.UpdateDraft(ctx, UpdateDraft{
		SessionID: "s1", BaseVersion: 0, EntryID: "e1", ClientMsgID: "m2",
		Mutate: func(e *domain.Entry) { e.Body = "this must not land" },
	})
	if !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale update = %v, want ErrStaleVersion", err)
	}
	stored, err := s.Entry(ctx, "e1")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if stored.Body != "the body the first edit wrote" {
		t.Errorf("a stale edit changed the body to %q", stored.Body)
	}
	if got := kindsOf(t, s, "s1"); len(got) != 1 {
		t.Errorf("a stale edit appended events: %v", got)
	}
	if _, err := s.ClientMessage(ctx, "s1", "m2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stale edit remembered its client message: %v", err)
	}
}

func TestUpdateDraftRefusesASavedEntryAndAMissingOne(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	mustUpdate(t, s, 0, &seasonChange{rating: 18})
	// A saved entry is no longer a draft, so the statement's own WHERE clause refuses the change. The session is
	// still open here, which is the only way to reach the draft guard rather than the version check.
	if _, err := s.SaveEntry(ctx, sampleEntry("e1", "w1", 18)); err != nil {
		t.Fatalf("SaveEntry: %v", err)
	}
	_, _, _, err := s.UpdateDraft(ctx, UpdateDraft{
		SessionID: "s1", BaseVersion: 1, EntryID: "e1", ClientMsgID: "m9",
		Mutate: func(e *domain.Entry) { e.Body = "after the save" },
	})
	if err == nil || !strings.Contains(err.Error(), "not a draft") {
		t.Fatalf("update of a saved entry = %v, want a not-a-draft refusal", err)
	}

	// An entry that does not exist is ErrNotFound, and the version still comes back untouched.
	s2, _ := draftRig(t)
	mustUpdate(t, s2, 0, &seasonChange{rating: 18})
	_, _, _, missingErr := s2.UpdateDraft(ctx, UpdateDraft{
		SessionID: "s1", BaseVersion: 1, EntryID: "nope", ClientMsgID: "m10",
		Mutate: func(*domain.Entry) {},
	})
	if !errors.Is(missingErr, ErrNotFound) {
		t.Fatalf("update of a missing entry = %v, want ErrNotFound", missingErr)
	}
	sess, err := s2.Session(ctx, "s1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.DraftVersion != 1 {
		t.Errorf("version after a failed update = %d, want 1", sess.DraftVersion)
	}
}

func TestConfirmDraftIsTheOnlyCommit(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	mustUpdate(t, s, 0, &seasonChange{rating: 18})

	entry, version, events, err := s.ConfirmDraft(ctx, Confirm{
		SessionID: "s1", BaseVersion: 1, Entry: sampleEntry("e1", "w1", 18), ClientMsgID: "c1",
	})
	if err != nil {
		t.Fatalf("ConfirmDraft: %v", err)
	}
	if version != 2 || entry.Status != domain.StatusSaved {
		t.Fatalf("confirm = %+v at version %d", entry, version)
	}
	if len(events) != 2 || events[0].Kind != EventDraftSnapshot || events[1].Kind != EventSaved {
		t.Fatalf("events = %+v", events)
	}
	var savedEvent struct {
		EntryID string `json:"entry_id"`
		Version int    `json:"version"`
	}
	if decodeErr := json.Unmarshal(events[1].Payload, &savedEvent); decodeErr != nil {
		t.Fatalf("saved payload: %v", decodeErr)
	}
	if savedEvent.EntryID != "e1" || savedEvent.Version != 2 {
		t.Errorf("saved payload = %+v", savedEvent)
	}

	sess, err := s.Session(ctx, "s1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.State != domain.SessionSaved {
		t.Errorf("session state = %q, want saved", sess.State)
	}
	saved, err := s.Entry(ctx, "e1")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if saved.Status != domain.StatusSaved {
		t.Errorf("entry status = %q", saved.Status)
	}
	// The first confirm has nothing to revise, and a saved entry appears in the library, not the drafts list.
	if revisions, revErr := s.Revisions(ctx, "e1"); revErr != nil || len(revisions) != 0 {
		t.Errorf("revisions after a first confirm = %+v, %v", revisions, revErr)
	}
	library, _, err := s.Library(ctx, domain.Cursor{}, 10)
	if err != nil {
		t.Fatalf("Library: %v", err)
	}
	if len(library) != 1 || library[0].ID != "e1" {
		t.Errorf("library = %+v", library)
	}
}

func TestConfirmDraftThatRevisesWritesTheRevisionAndRetiresTheCopy(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	mustUpdate(t, s, 0, &seasonChange{rating: 18})
	if _, _, _, err := s.ConfirmDraft(ctx, Confirm{
		SessionID: "s1", BaseVersion: 1, Entry: sampleEntry("e1", "w1", 18), ClientMsgID: "c1",
	}); err != nil {
		t.Fatalf("ConfirmDraft: %v", err)
	}

	// A second session edits the saved review: its working copy is written onto the saved entry and then retired.
	if err := s.CreateSession(ctx, domain.Session{
		ID: "s2", Kind: domain.SessionEntry, State: domain.SessionActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.CreateDraft(ctx, sampleEntry("e2", "w1", 20)); err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	edited := sampleEntry("e2", "w1", 20)
	edited.Body = "the world felt so lived in, and the ending landed"
	entry, version, events, err := s.ConfirmDraft(ctx, Confirm{
		SessionID: "s2", BaseVersion: 0, Entry: edited, ClientMsgID: "c2", Revises: "e1",
	})
	if err != nil {
		t.Fatalf("ConfirmDraft (revision): %v", err)
	}
	if entry.ID != "e1" || version != 1 {
		t.Fatalf("revision confirm returned %q at version %d", entry.ID, version)
	}
	if len(events) != 2 || events[1].Kind != EventSaved {
		t.Fatalf("events = %+v", events)
	}
	revisions, err := s.Revisions(ctx, "e1")
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revisions) != 1 || revisions[0].Snapshot.Rating != 18 {
		t.Fatalf("revisions = %+v, want the previous 18 quarters", revisions)
	}
	// The working copy is soft-deleted, never removed, and the saved entry carries the new body.
	retired, err := s.Entry(ctx, "e2")
	if err != nil {
		t.Fatalf("Entry (working copy): %v", err)
	}
	if !retired.Deleted {
		t.Errorf("the working copy was not retired: %+v", retired)
	}
	stored, err := s.Entry(ctx, "e1")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if stored.Rating != 20 || !strings.Contains(stored.Body, "the ending landed") {
		t.Errorf("saved entry = %+v", stored)
	}
}

func TestConfirmDraftRefusesAStaleVersion(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	mustUpdate(t, s, 0, &seasonChange{rating: 18})
	if _, _, _, err := s.ConfirmDraft(ctx, Confirm{
		SessionID: "s1", BaseVersion: 0, Entry: sampleEntry("e1", "w1", 18), ClientMsgID: "c1",
	}); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale confirm = %v, want ErrStaleVersion", err)
	}
	stored, err := s.Entry(ctx, "e1")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if stored.Status != domain.StatusDraft {
		t.Errorf("a stale confirm saved the entry: %+v", stored)
	}
	if got := kindsOf(t, s, "s1"); len(got) != 1 {
		t.Errorf("a stale confirm appended events: %v", got)
	}
}

func TestDiscardDraftClosesTheSessionOnce(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	if err := s.DiscardDraft(ctx, "s1", "d1"); err != nil {
		t.Fatalf("DiscardDraft: %v", err)
	}
	sess, err := s.Session(ctx, "s1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.State != domain.SessionDiscarded {
		t.Errorf("session state = %q, want discarded", sess.State)
	}
	if got := kindsOf(t, s, "s1"); len(got) != 1 || got[0] != EventDiscarded {
		t.Errorf("events = %v", got)
	}

	// A discard retried after a reconnect finds its client message and changes nothing the second time.
	err = s.DiscardDraft(ctx, "s1", "d1")
	if !errors.Is(err, ErrDuplicateClientMessage) {
		t.Fatalf("repeated discard = %v, want ErrDuplicateClientMessage", err)
	}
	if got := kindsOf(t, s, "s1"); len(got) != 1 {
		t.Errorf("a repeated discard appended events: %v", got)
	}
}

func TestAcceptDraftSourcesAcceptsOnce(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	web := domain.Source{Provider: "web", ProviderID: "https://example.test/dune", Trust: domain.TrustUnverified}
	if _, err := s.AddSource(ctx, "w1", web); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	fields := []domain.Source{{Provider: web.Provider, ProviderID: web.ProviderID}}

	entry, version, event, err := s.AcceptDraftSources(ctx, AcceptSources{
		SessionID: "s1", BaseVersion: 0, EntryID: "e1", WorkID: "w1", Fields: fields, ClientMsgID: "a1",
	})
	if err != nil {
		t.Fatalf("AcceptDraftSources: %v", err)
	}
	if version != 1 || entry.ID != "e1" || event.Kind != EventDraftSnapshot {
		t.Fatalf("accept = %+v at version %d, event %+v", entry, version, event)
	}
	sources, err := s.Sources(ctx, "w1")
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) != 1 || sources[0].Trust != domain.TrustAccepted {
		t.Fatalf("sources = %+v, want one accepted source", sources)
	}

	// The tap is the guard: a second accept of the same field is a refusal, not a silent success.
	_, _, _, secondErr := s.AcceptDraftSources(ctx, AcceptSources{
		SessionID: "s1", BaseVersion: 1, EntryID: "e1", WorkID: "w1", Fields: fields, ClientMsgID: "a2",
	})
	if !errors.Is(secondErr, ErrNotAcceptable) {
		t.Fatalf("second accept = %v, want ErrNotAcceptable", secondErr)
	}
	sess, err := s.Session(ctx, "s1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.DraftVersion != 1 {
		t.Errorf("version after a refused accept = %d, want 1", sess.DraftVersion)
	}

	// A stale tap is refused before anything is accepted, and a tap on a missing card is ErrNotFound.
	if _, _, _, err := s.AcceptDraftSources(ctx, AcceptSources{
		SessionID: "s1", BaseVersion: 0, EntryID: "e1", WorkID: "w1", Fields: fields, ClientMsgID: "a3",
	}); !errors.Is(err, ErrStaleVersion) {
		t.Errorf("stale accept = %v, want ErrStaleVersion", err)
	}
	if _, _, _, err := s.AcceptDraftSources(ctx, AcceptSources{
		SessionID: "s1", BaseVersion: 1, EntryID: "nope", WorkID: "w1", Fields: nil, ClientMsgID: "a4",
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("accept on a missing card = %v, want ErrNotFound", err)
	}
}

// seasonChange is the smallest edit the tests make: a rating, so the card is confirmable.
type seasonChange struct{ rating int }

// mustUpdate applies one edit at base version base and fails the test if it is refused.
func mustUpdate(t *testing.T, s *Store, base int, change *seasonChange) {
	t.Helper()
	_, _, _, err := s.UpdateDraft(context.Background(), UpdateDraft{
		SessionID: "s1", BaseVersion: base, EntryID: "e1", ClientMsgID: "edit-" + itoa(base),
		Mutate: func(e *domain.Entry) {
			e.Rating = change.rating
			e.Body = "the body the first edit wrote"
			e.BodyOrigin = domain.BodyTyped
		},
	})
	if err != nil {
		t.Fatalf("UpdateDraft at version %d: %v", base, err)
	}
}

func TestDraftEntryTagsAreReadInNameOrder(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	if err := raw(t, s, `INSERT INTO tags (id, name) VALUES ('t1', 'quiet'), ('t2', 'atmospheric')`); err != nil {
		t.Fatalf("seed tags: %v", err)
	}
	if err := raw(t, s, `INSERT INTO entry_tags (entry_id, tag_id, source) VALUES
		('e1', 't1', 'mine'), ('e1', 't2', 'genre')`); err != nil {
		t.Fatalf("seed entry tags: %v", err)
	}
	tags, err := s.EntryTags(ctx, "e1")
	if err != nil {
		t.Fatalf("EntryTags: %v", err)
	}
	if len(tags) != 2 || tags[0].Name != "atmospheric" || tags[1].Name != "quiet" {
		t.Fatalf("tags = %+v, want them in name order", tags)
	}
	if tags[0].Source != "genre" {
		t.Errorf("tag source = %q", tags[0].Source)
	}
	if tags, err := s.EntryTags(ctx, "nope"); err != nil || len(tags) != 0 {
		t.Errorf("tags for a missing entry = %+v, %v", tags, err)
	}
}

// A confirm writes the card she confirmed, not a snapshot of it taken when the draft was created. Every field the
// card can edit is on the saved row, which is the bug this test was written for: season, and the work a re-pick
// chose, used to be dropped by the save statement and only the draft-time values survived.
func TestConfirmWritesEveryFieldTheCardHolds(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	if err := s.SaveWork(ctx, sampleWork("w2")); err != nil {
		t.Fatalf("SaveWork: %v", err)
	}
	season := 3
	if _, _, _, err := s.UpdateDraft(ctx, UpdateDraft{
		SessionID: "s1", BaseVersion: 0, EntryID: "e1", ClientMsgID: "m1",
		Mutate: func(e *domain.Entry) {
			e.WorkID = "w2"
			e.Rating = 16
			e.Season = &season
			e.Favourite = true
			e.Verdict = "warm"
			e.Format = domain.FormatHardback
			e.FinishedOn = "2026-10-30"
			e.Body = "the last chapter is the one I keep thinking about"
			e.BodyOrigin = domain.BodyTyped
		},
	}); err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	card, err := s.Entry(ctx, "e1")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if _, _, _, confirmErr := s.ConfirmDraft(ctx, Confirm{
		SessionID: "s1", BaseVersion: 1, Entry: card, ClientMsgID: "c1",
	}); confirmErr != nil {
		t.Fatalf("ConfirmDraft: %v", confirmErr)
	}
	saved, err := s.Entry(ctx, "e1")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if saved.WorkID != "w2" {
		t.Errorf("work_id = %q, want the work the card held", saved.WorkID)
	}
	if saved.Season == nil || *saved.Season != 3 {
		t.Errorf("season = %v, want 3", saved.Season)
	}
	if saved.Rating != 16 || !saved.Favourite || saved.Verdict != "warm" || saved.Format != domain.FormatHardback {
		t.Errorf("saved = %+v", saved)
	}
	if saved.FinishedOn != "2026-10-30" || !strings.Contains(saved.Body, "last chapter") {
		t.Errorf("saved = %+v", saved)
	}
}

// A discard of a session that is not there fails rather than reporting a close that never happened, and one with no
// client message still writes its event: the ledger row is optional, the close is not.
func TestDiscardDraftWithoutAClientMessage(t *testing.T) {
	s, _ := draftRig(t)
	ctx := context.Background()
	if err := s.DiscardDraft(ctx, "nope", "d1"); err == nil {
		t.Fatal("a discard of a missing session was reported as done")
	}
	if err := s.DiscardDraft(ctx, "s1", ""); err != nil {
		t.Fatalf("DiscardDraft without a client message: %v", err)
	}
	if got := kindsOf(t, s, "s1"); len(got) != 1 || got[0] != EventDiscarded {
		t.Errorf("events = %v", got)
	}
}
