package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/store"
)

// draftSession makes an active entry session with a draft linked to a saved work, which is the state every draft
// operation starts from.
func draftSession(t *testing.T) (*Engine, *store.Store, domain.Session) {
	t.Helper()
	e, db := newEngine(t)
	ctx := context.Background()
	if err := db.SaveWork(ctx, domain.Work{ID: "w1", Medium: "book", Title: "The Lantern Keeper", Verified: true}); err != nil {
		t.Fatalf("SaveWork: %v", err)
	}
	return e, db, draftFor(t, e, db, "w1")
}

func patch(kv map[string]any) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(kv))
	for key, value := range kv {
		body, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		out[key] = body
	}
	return out
}

// --- edit -----------------------------------------------------------------------------------------------

func TestEditDraftBumpsTheVersionAndKeepsTheCardWhole(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()

	draft, err := e.EditDraft(ctx, sess.ID, Edit{BaseVersion: 0, Set: patch(map[string]any{
		"rating": 17, "body": "A quiet book about light and memory.", "verdict": "Lovely",
		"finished_on": "2026-10-30", "favourite": true,
	})})
	if err != nil {
		t.Fatalf("EditDraft: %v", err)
	}
	if draft.Version != 1 {
		t.Errorf("version = %d, want 1", draft.Version)
	}
	entry := draft.Entry
	if entry.Rating != 17 || entry.Body != "A quiet book about light and memory." || entry.Verdict != "Lovely" ||
		entry.FinishedOn != "2026-10-30" || !entry.Favourite {
		t.Errorf("entry = %+v", entry)
	}
	if entry.BodyOrigin != domain.BodyTyped || len(entry.BodySpans) != 0 {
		t.Errorf("origin = %q, spans = %v; a typed body is hers, with no spans", entry.BodyOrigin, entry.BodySpans)
	}

	// The snapshot event carries the new card, which is what a resuming client redraws.
	events, err := e.store.EventsSince(ctx, sess.ID, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(events) != 1 || events[0].Kind != store.EventDraftSnapshot {
		t.Fatalf("events = %+v", events)
	}
	var snapshot struct {
		Version int          `json:"version"`
		Draft   domain.Entry `json:"draft"`
	}
	if err := json.Unmarshal(events[0].Payload, &snapshot); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Version != 1 || snapshot.Draft.Rating != 17 {
		t.Errorf("snapshot = %+v", snapshot)
	}
}

// A stale version is refused and changes nothing, which is the whole point of the version.
func TestEditDraftRefusesAStaleVersion(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	if _, err := e.EditDraft(ctx, sess.ID, Edit{BaseVersion: 0, Set: patch(map[string]any{"rating": 12})}); err != nil {
		t.Fatalf("first edit: %v", err)
	}
	_, staleErr := e.EditDraft(ctx, sess.ID, Edit{BaseVersion: 0, Set: patch(map[string]any{"rating": 4})})
	var refusal *Refusal
	if !errors.As(staleErr, &refusal) || refusal.Code != CodeStaleVersion {
		t.Fatalf("stale edit = %v, want a stale_version refusal", staleErr)
	}
	draft, err := e.Draft(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if draft.Entry.Rating != 12 || draft.Version != 1 {
		t.Errorf("the stale edit changed the card: %+v", draft)
	}
}

func TestEditDraftRefusesUnknownFieldsAndBadValues(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	cases := map[string]struct {
		set  map[string]json.RawMessage
		code string
	}{
		"unknown field":   {patch(map[string]any{"colour": "blue"}), CodeUnknownField},
		"rating too high": {patch(map[string]any{"rating": 21}), CodeInvalidValue},
		"rating negative": {patch(map[string]any{"rating": -1}), CodeInvalidValue},
		"body too long":   {patch(map[string]any{"body": strings.Repeat("a", domain.MaxBodyLength+1)}), CodeInvalidValue},
		"season zero":     {patch(map[string]any{"season": 0}), CodeInvalidValue},
		"rating as text":  {map[string]json.RawMessage{"rating": json.RawMessage(`"five"`)}, CodeInvalidValue},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.EditDraft(ctx, sess.ID, Edit{BaseVersion: 0, Set: tc.set})
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != tc.code {
				t.Fatalf("edit = %v, want %s", err, tc.code)
			}
		})
	}
}

// --- confirm --------------------------------------------------------------------------------------------

func TestConfirmSavesOnce(t *testing.T) {
	e, db, sess := draftSession(t)
	ctx := context.Background()
	draft, err := e.EditDraft(ctx, sess.ID, Edit{BaseVersion: 0, Set: patch(map[string]any{
		"rating": 17, "body": "A quiet book about light and memory.",
	})})
	if err != nil {
		t.Fatalf("EditDraft: %v", err)
	}

	entry, version, events, err := e.Confirm(ctx, sess.ID, ConfirmRequest{BaseVersion: draft.Version, ClientMsgID: "m1"})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if entry.Status != domain.StatusSaved || entry.Rating != 17 {
		t.Errorf("entry = %+v", entry)
	}
	if version != draft.Version+1 {
		t.Errorf("version = %d, want %d", version, draft.Version+1)
	}
	if len(events) != 2 || events[0].Kind != store.EventDraftSnapshot || events[1].Kind != store.EventSaved {
		t.Errorf("events = %+v", events)
	}

	// One save: a second confirm cannot find an active session or a matching version.
	if _, _, _, secondErr := e.Confirm(ctx, sess.ID, ConfirmRequest{BaseVersion: version, ClientMsgID: "m2"}); secondErr == nil {
		t.Fatal("a second confirm saved again")
	}
	closed, err := db.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if closed.State != domain.SessionSaved || closed.EntryID != entry.ID {
		t.Errorf("session = %+v", closed)
	}
	if _, err := db.Entry(ctx, entry.ID); err != nil {
		t.Fatalf("the saved entry is not readable: %v", err)
	}
}

// The guard runs before anything is written: no rating, no body, or a body that is not hers is refused, and the
// draft is untouched.
func TestConfirmRefusesWhatTheGuardRefuses(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	draft, err := e.Draft(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if _, _, _, err := e.Confirm(ctx, sess.ID, ConfirmRequest{BaseVersion: draft.Version}); err == nil {
		t.Fatal("a draft with no rating was saved")
	} else {
		var guard *domain.GuardError
		if !errors.As(err, &guard) || guard.Code != domain.CodeRatingRequired {
			t.Fatalf("error = %v, want rating_required", err)
		}
	}

	if _, err := e.EditDraft(ctx, sess.ID, Edit{BaseVersion: 0, Set: patch(map[string]any{"rating": 12})}); err != nil {
		t.Fatalf("EditDraft: %v", err)
	}
	if _, _, _, err := e.Confirm(ctx, sess.ID, ConfirmRequest{BaseVersion: 1}); err == nil {
		t.Fatal("a draft with no body was saved")
	}
}

// An extracted body has to be found in her messages. The engine passes them to the guard, which does the proving.
func TestConfirmProvesAnExtractedBody(t *testing.T) {
	e, db, sess := draftSession(t)
	ctx := context.Background()
	source := "I loved the lantern keeper, it stayed with me for days."
	entry, err := db.Entry(ctx, "e-"+sess.ID)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	entry.Rating = 16
	entry.Body = "it stayed with me for days"
	entry.BodyOrigin = domain.BodyExtracted
	spans := []domain.Span{{Start: strings.Index(source, "it stayed"), End: strings.Index(source, "days") + len("days"), Text: entry.Body}}
	entry.BodySpans = spans
	if err := db.CreateDraft(ctx, entry); err == nil {
		t.Fatal("the draft helper already created this entry")
	}
	// The draft exists; write the fields through the store's own draft update, then confirm with her words.
	if _, _, _, err := e.store.UpdateDraft(ctx, store.UpdateDraft{
		SessionID: sess.ID, BaseVersion: 0, EntryID: entry.ID,
		Mutate: func(e *domain.Entry) {
			e.Rating = 16
			e.Body = entry.Body
			e.BodyOrigin = domain.BodyExtracted
			e.BodySpans = spans
		},
	}); err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if _, _, _, err := e.Confirm(ctx, sess.ID, ConfirmRequest{BaseVersion: 1, SourceText: "something else entirely"}); err == nil {
		t.Fatal("a body that is not in her words was saved")
	}
	if _, _, _, err := e.Confirm(ctx, sess.ID, ConfirmRequest{BaseVersion: 1, SourceText: source}); err != nil {
		t.Fatalf("Confirm with the real words: %v", err)
	}
}

// --- discard and limits ---------------------------------------------------------------------------------

func TestDiscardClosesAndKeepsTheDraft(t *testing.T) {
	e, db, sess := draftSession(t)
	ctx := context.Background()
	if err := e.Discard(ctx, sess.ID); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	closed, err := db.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if closed.State != domain.SessionDiscarded {
		t.Errorf("state = %q", closed.State)
	}
	// Nothing is deleted: the draft is still there to look at.
	if _, err := db.Entry(ctx, "e-"+sess.ID); err != nil {
		t.Errorf("the discarded draft is gone: %v", err)
	}
	if err := e.Discard(ctx, sess.ID); !errors.Is(err, ErrClosed) {
		t.Errorf("second discard = %v, want ErrClosed", err)
	}
}

func TestMessageLimits(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()

	if _, err := e.AppendUserMessage(ctx, sess.ID, strings.Repeat("a", MaxMessageLength+1), "m-long"); err == nil {
		t.Fatal("an oversized message was stored")
	} else {
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeMessageLong {
			t.Fatalf("error = %v, want message_too_long", err)
		}
	}
	if _, err := e.AppendUserMessage(ctx, sess.ID, "   ", "m-empty"); err == nil {
		t.Fatal("an empty message was stored")
	}

	event, err := e.AppendUserMessage(ctx, sess.ID, "I finished it last night.", "m1")
	if err != nil {
		t.Fatalf("AppendUserMessage: %v", err)
	}
	if event.Kind != store.EventUserMessage {
		t.Errorf("event = %+v", event)
	}
	// A retry of the same client message is a duplicate, not a second message.
	if _, err := e.AppendUserMessage(ctx, sess.ID, "I finished it last night.", "m1"); !errors.Is(err, store.ErrDuplicateClientMessage) {
		t.Errorf("retry = %v, want the ledger to refuse it", err)
	}
}

// --- accepting a web field ------------------------------------------------------------------------------

// webField attaches one unverified source to the draft's work and returns its id, the shape a web lookup leaves.
func webField(t *testing.T, db *store.Store, workID string) string {
	t.Helper()
	id, err := db.AddSource(context.Background(), workID, domain.Source{
		Provider: "web", ProviderID: "w-1", Trust: domain.TrustUnverified,
		Title: "The Lantern Keeper", Year: 1999,
	})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	return id
}

func TestAcceptFieldsMakesAWebFactCanonical(t *testing.T) {
	e, db, sess := draftSession(t)
	ctx := context.Background()
	field := webField(t, db, "w1")

	draft, err := e.AcceptFields(ctx, sess.ID, 0, []string{"web|w-1"}, "m-accept")
	if err != nil {
		t.Fatalf("AcceptFields: %v", err)
	}
	if draft.Version != 1 {
		t.Errorf("version = %d, want 1", draft.Version)
	}
	sources, err := db.Sources(ctx, "w1")
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	accepted := false
	for _, source := range sources {
		if source.Provider == "web" && source.Trust == domain.TrustAccepted {
			accepted = true
		}
	}
	if !accepted {
		t.Errorf("sources = %+v, want the web field accepted", sources)
	}
	if field == "" {
		t.Error("the fixture did not store a source")
	}

	// The snapshot event is there, and so is the ledger row.
	events, err := db.EventsSince(ctx, sess.ID, 0)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	if len(events) != 1 || events[0].Kind != store.EventDraftSnapshot {
		t.Fatalf("events = %+v", events)
	}
	if msg, seen, err := e.Seen(ctx, sess.ID, "m-accept"); err != nil || !seen || msg.Seq != events[0].Seq {
		t.Errorf("ledger = %+v, seen=%v, err=%v", msg, seen, err)
	}
}

func TestAcceptFieldsRefusesTheSecondTap(t *testing.T) {
	e, db, sess := draftSession(t)
	ctx := context.Background()
	webField(t, db, "w1")
	if _, err := e.AcceptFields(ctx, sess.ID, 0, []string{"web|w-1"}, "m1"); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	_, err := e.AcceptFields(ctx, sess.ID, 1, []string{"web|w-1"}, "m2")
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeAlreadyAccepted {
		t.Fatalf("second accept = %v, want already_accepted", err)
	}

	// A stale version and a field that is not on this work are refused before anything is touched.
	if _, err := e.AcceptFields(ctx, sess.ID, 0, []string{"web|w-1"}, "m3"); !errors.As(err, &refusal) || refusal.Code != CodeStaleVersion {
		t.Errorf("stale accept = %v, want stale_version", err)
	}
	if _, err := e.AcceptFields(ctx, sess.ID, 1, []string{"tmdb|movie:101"}, "m4"); !errors.As(err, &refusal) || refusal.Code != CodeInvalidValue {
		t.Errorf("foreign field = %v, want invalid_value", err)
	}
	if _, err := e.AcceptFields(ctx, sess.ID, 1, nil, "m5"); !errors.As(err, &refusal) || refusal.Code != CodeInvalidValue {
		t.Errorf("empty accept = %v, want invalid_value", err)
	}
}
