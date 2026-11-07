package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/AutumnsGrove/sepia/internal/domain"
)

// newSession opens a store and an active entry session, which is the fixture every engine test starts from.
func newSession(t *testing.T) (*Store, domain.Session) {
	t.Helper()
	s := openTest(t)
	ctx := context.Background()
	sess := domain.Session{ID: "s1", Kind: domain.SessionEntry, State: domain.SessionActive}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, err := s.Session(ctx, "s1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	return s, got
}

func TestOpenInteractionIsAtomic(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()

	interaction, event, err := s.OpenInteraction(ctx, Interaction{
		SessionID: "s1", Kind: "describe", Payload: json.RawMessage(`{"categories":[]}`),
	})
	if err != nil {
		t.Fatalf("OpenInteraction: %v", err)
	}
	if event.Kind != EventInteractionRequest || event.Seq != interaction.OpenedSeq {
		t.Errorf("event = %+v, interaction opened at %d", event, interaction.OpenedSeq)
	}
	// The client draws the question from the event, so the event names the question and its kind, not the payload
	// alone (docs/PROTOCOL.md 3).
	var wire struct {
		InteractionID string          `json:"interaction_id"`
		Kind          string          `json:"kind"`
		Payload       json.RawMessage `json:"payload"`
	}
	if decodeErr := json.Unmarshal(event.Payload, &wire); decodeErr != nil {
		t.Fatalf("event payload: %v", decodeErr)
	}
	if wire.InteractionID != interaction.ID || wire.Kind != "describe" || len(wire.Payload) == 0 {
		t.Errorf("event payload = %+v", wire)
	}

	open, err := s.OpenInteractions(ctx, "s1")
	if err != nil {
		t.Fatalf("OpenInteractions: %v", err)
	}
	if len(open) != 1 || open[0].ID != interaction.ID || !open[0].Open() {
		t.Fatalf("open = %+v", open)
	}
}

func TestResolveInteractionOnce(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	interaction, _, err := s.OpenInteraction(ctx, Interaction{SessionID: "s1", Kind: "question_form", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("OpenInteraction: %v", err)
	}

	resolved, event, err := s.ResolveInteraction(ctx, "s1", interaction.ID, domain.ResolvedByUser,
		json.RawMessage(`{"answers":[]}`), "m1")
	if err != nil {
		t.Fatalf("ResolveInteraction: %v", err)
	}
	if event.Kind != EventInteractionResolved || resolved.ResolvedSeq == nil || *resolved.ResolvedSeq != event.Seq {
		t.Errorf("resolved = %+v, event = %+v", resolved, event)
	}

	// The first answer wins: a second changes nothing, appends nothing, and remembers nothing new.
	if _, _, secondErr := s.ResolveInteraction(ctx, "s1", interaction.ID, domain.ResolvedByUser,
		json.RawMessage(`{"answers":[1]}`), "m2"); !errors.Is(secondErr, ErrInteractionResolved) {
		t.Fatalf("second resolve = %v, want ErrInteractionResolved", secondErr)
	}
	head, err := s.HeadSequence(ctx, "s1")
	if err != nil {
		t.Fatalf("HeadSequence: %v", err)
	}
	if head != 2 {
		t.Errorf("head = %d, want 2 (the request and one resolution)", head)
	}
	if open, err := s.OpenInteractions(ctx, "s1"); err != nil || len(open) != 0 {
		t.Errorf("open after resolving = %+v, %v", open, err)
	}

	// The ledger remembers only the first outcome.
	if _, err := s.ClientMessage(ctx, "s1", "m2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second outcome was remembered: %v", err)
	}
	if m, err := s.ClientMessage(ctx, "s1", "m1"); err != nil || m.Status != "accepted" {
		t.Errorf("first outcome = %+v, %v", m, err)
	}
}

func TestInteractionsAreNeverDeletedOrReopened(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	interaction, _, err := s.OpenInteraction(ctx, Interaction{SessionID: "s1", Kind: "fill_gap", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("OpenInteraction: %v", err)
	}
	if _, _, err := s.ResolveInteraction(ctx, "s1", interaction.ID, domain.ResolvedByServer, json.RawMessage(`null`), ""); err != nil {
		t.Fatalf("ResolveInteraction: %v", err)
	}

	// The database refuses to reopen or delete, whatever the code above it believes.
	reopen := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE interactions SET resolved_seq = NULL, resolved_by = NULL WHERE id = ?`, interaction.ID)
		return err
	})
	if reopen == nil {
		t.Error("the database allowed a resolved interaction to reopen")
	}
	remove := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM interactions WHERE id = ?`, interaction.ID)
		return err
	})
	if remove == nil {
		t.Error("the database allowed an interaction to be deleted")
	}
}

func TestBumpDraftVersion(t *testing.T) {
	s, sess := newSession(t)
	ctx := context.Background()
	if sess.DraftVersion != 0 {
		t.Fatalf("new session version = %d", sess.DraftVersion)
	}

	next, err := s.BumpDraftVersion(ctx, "s1", 0)
	if err != nil {
		t.Fatalf("BumpDraftVersion: %v", err)
	}
	if next != 1 {
		t.Errorf("version = %d, want 1", next)
	}

	// A stale base changes nothing, and says so.
	if _, err := s.BumpDraftVersion(ctx, "s1", 0); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale bump = %v, want ErrStaleVersion", err)
	}
	got, sessionErr := s.Session(ctx, "s1")
	if sessionErr != nil {
		t.Fatalf("Session: %v", sessionErr)
	}
	if got.DraftVersion != 1 {
		t.Errorf("version after a stale bump = %d, want 1", got.DraftVersion)
	}

	// A closed session cannot be edited either.
	if err := s.SetSessionState(ctx, "s1", domain.SessionSaved); err != nil {
		t.Fatalf("SetSessionState: %v", err)
	}
	if _, err := s.BumpDraftVersion(ctx, "s1", 1); !errors.Is(err, ErrStaleVersion) {
		t.Errorf("bump on a closed session = %v, want ErrStaleVersion", err)
	}
}

func TestClientMessageLedger(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	msg := domain.ClientMessage{SessionID: "s1", ClientMsgID: "abc", Seq: 4, Status: "accepted"}
	if err := s.RememberClient(ctx, msg); err != nil {
		t.Fatalf("RememberClient: %v", err)
	}
	if err := s.RememberClient(ctx, msg); !errors.Is(err, ErrDuplicateClientMessage) {
		t.Fatalf("second RememberClient = %v, want ErrDuplicateClientMessage", err)
	}
	got, err := s.ClientMessage(ctx, "s1", "abc")
	if err != nil {
		t.Fatalf("ClientMessage: %v", err)
	}
	if got.Seq != 4 || got.Status != "accepted" {
		t.Errorf("ledger row = %+v", got)
	}
	if _, err := s.ClientMessage(ctx, "s1", "never"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id = %v", err)
	}
}

// The two tables are per session: the same client_msg_id in another session is a different message, because a
// session is the unit of a conversation.
func TestLedgerIsPerSession(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	if err := s.CreateSession(ctx, domain.Session{ID: "s2", Kind: domain.SessionGeneral, State: domain.SessionActive}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, session := range []string{"s1", "s2"} {
		if err := s.RememberClient(ctx, domain.ClientMessage{SessionID: session, ClientMsgID: "same", Status: "accepted"}); err != nil {
			t.Fatalf("RememberClient(%s): %v", session, err)
		}
	}
}

// The record of what was asked is readable after the fact: one question by id, every question for a session, and a
// missing id told apart from an answered one.
func TestInteractionsAreReadableAfterTheFact(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	first, _, err := s.OpenInteraction(ctx, Interaction{SessionID: "s1", Kind: "describe"})
	if err != nil {
		t.Fatalf("OpenInteraction: %v", err)
	}
	second, _, err := s.OpenInteraction(ctx, Interaction{SessionID: "s1", Kind: "fill_gap"})
	if err != nil {
		t.Fatalf("OpenInteraction: %v", err)
	}
	if _, _, resolveErr := s.ResolveInteraction(ctx, "s1", first.ID, domain.ResolvedByUser,
		json.RawMessage(`{"answers":["quiet"]}`), "m1"); resolveErr != nil {
		t.Fatalf("ResolveInteraction: %v", resolveErr)
	}

	one, err := s.Interaction(ctx, second.ID)
	if err != nil {
		t.Fatalf("Interaction: %v", err)
	}
	if one.Kind != "fill_gap" || one.SessionID != "s1" || one.ResolvedSeq != nil || !one.Open() {
		t.Errorf("open question = %+v", one)
	}
	if one.OpenedAt.IsZero() {
		t.Error("the question lost its opened_at")
	}

	all, err := s.Interactions(ctx, "s1")
	if err != nil {
		t.Fatalf("Interactions: %v", err)
	}
	if len(all) != 2 || all[0].ID != first.ID || all[1].ID != second.ID {
		t.Fatalf("interactions = %+v, want both in the order they were asked", all)
	}
	if all[0].ResolvedBy != domain.ResolvedByUser || all[0].ResolvedSeq == nil {
		t.Errorf("the answered question lost its answer: %+v", all[0])
	}
	if string(all[0].Resolution) != `{"answers":["quiet"]}` {
		t.Errorf("resolution = %s", all[0].Resolution)
	}

	// A question that was never asked is ErrNotFound, not an empty struct.
	if _, err := s.Interaction(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing interaction = %v, want ErrNotFound", err)
	}
}

// The conversation outlives the event log, because the log is compacted and a resumed screen still wants the words.
func TestSetMessagesStoresTheConversation(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	if err := s.SetMessages(ctx, "s1", json.RawMessage(`[{"role":"user","content":"hello"}]`)); err != nil {
		t.Fatalf("SetMessages: %v", err)
	}
	got, err := s.Session(ctx, "s1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if string(got.Messages) != `[{"role":"user","content":"hello"}]` {
		t.Fatalf("messages = %s", got.Messages)
	}
	// An empty value is an empty conversation, never the string "null" a client would choke on.
	if err := s.SetMessages(ctx, "s1", nil); err != nil {
		t.Fatalf("SetMessages (empty): %v", err)
	}
	if got, err := s.Session(ctx, "s1"); err != nil || string(got.Messages) != "[]" {
		t.Errorf("messages after an empty write = %s, %v", got.Messages, err)
	}
}

// A question belongs to the session that asked it. Answering it from somewhere else is a miss, not a change: the
// row exists, but it is not this conversation's to resolve.
func TestResolveInteractionRefusesAnotherSessionsQuestion(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	if err := s.CreateSession(ctx, domain.Session{ID: "s2", Kind: domain.SessionEntry, State: domain.SessionActive}); err != nil {
		t.Fatal(err)
	}
	interaction, _, err := s.OpenInteraction(ctx, Interaction{SessionID: "s1", Kind: "describe"})
	if err != nil {
		t.Fatalf("OpenInteraction: %v", err)
	}
	_, _, wrongSession := s.ResolveInteraction(ctx, "s2", interaction.ID, domain.ResolvedByUser, nil, "")
	if !errors.Is(wrongSession, ErrNotFound) {
		t.Fatalf("resolve from another session = %v, want ErrNotFound", wrongSession)
	}
	still, err := s.Interaction(ctx, interaction.ID)
	if err != nil {
		t.Fatalf("Interaction: %v", err)
	}
	if !still.Open() {
		t.Errorf("the question was resolved by the wrong session: %+v", still)
	}
}

// An absent payload is an empty object, never the word "null": a client that validates its events would choke on it,
// and the kind alone is enough to draw a question or replay an event.
func TestAbsentPayloadsBecomeEmptyObjects(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	interaction, event, err := s.OpenInteraction(ctx, Interaction{SessionID: "s1", Kind: "describe"})
	if err != nil {
		t.Fatalf("OpenInteraction: %v", err)
	}
	if string(interaction.Payload) != "{}" {
		t.Errorf("interaction payload = %s", interaction.Payload)
	}
	if len(event.Payload) == 0 {
		t.Error("the interaction event carried no payload")
	}
	appended, err := s.AppendEvent(ctx, "s1", EventNotice, nil)
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if string(appended.Payload) != "{}" {
		t.Errorf("appended payload = %s", appended.Payload)
	}
	if appended.Seq != event.Seq+1 {
		t.Errorf("seq = %d, want %d", appended.Seq, event.Seq+1)
	}
}
