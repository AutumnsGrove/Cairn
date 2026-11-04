package session

import (
	"context"
	"strings"
	"testing"

	"github.com/AutumnsGrove/cairn/internal/domain"
)

func TestCardCarriesTheWorkTheAnswersAndWhatWasLeftOut(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	message := "hey so i finished the lantern keeper last night, the ending landed but it was too long"
	if _, err := e.AppendUserMessage(ctx, sess.ID, message, "m-source"); err != nil {
		t.Fatalf("AppendUserMessage: %v", err)
	}
	quote := "the ending landed but it was too long"
	draft, err := e.SetExtractedBody(ctx, sess.ID, 0, message, []string{quote}, "the ending landed", "m-body")
	if err != nil {
		t.Fatalf("SetExtractedBody: %v", err)
	}
	if draft.Entry.WorkID != "w1" {
		t.Fatalf("work = %q, want w1", draft.Entry.WorkID)
	}
	card, err := e.Card(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if card.Work.Title != "The Lantern Keeper" {
		t.Errorf("work = %+v", card.Work)
	}
	if len(card.LeftOut) == 0 || !strings.Contains(card.LeftOut[0].Text, "hey so i finished") {
		t.Errorf("left out = %+v, want the framing", card.LeftOut)
	}
	if card.SourceText != message {
		t.Errorf("source = %q", card.SourceText)
	}
}

func TestEntryViewReadsASavedEntry(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	if _, err := e.EditDraft(ctx, sess.ID, Edit{BaseVersion: 0, Set: patch(map[string]any{
		"rating": 18, "body": "A quiet book about light and memory.",
	})}); err != nil {
		t.Fatalf("EditDraft: %v", err)
	}
	entry, _, _, err := e.Confirm(ctx, sess.ID, ConfirmRequest{BaseVersion: 1})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	view, err := e.EntryView(ctx, entry.ID)
	if err != nil {
		t.Fatalf("EntryView: %v", err)
	}
	if view.Entry.Rating != 18 || view.Work.Title == "" {
		t.Errorf("view = %+v", view)
	}
	if _, err := e.EntryView(ctx, "no-such-entry"); err == nil {
		t.Error("a missing entry was read")
	}
}

func TestSessionsListsWithFilters(t *testing.T) {
	e, _, sess := draftSession(t)
	ctx := context.Background()
	general, err := e.CreateGeneral(ctx, "what should i read next")
	if err != nil {
		t.Fatal(err)
	}
	active, err := e.Sessions(ctx, domain.SessionActive, "", "", 10)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active = %d, want the draft and the chat", len(active))
	}
	chats, err := e.Sessions(ctx, "", domain.SessionGeneral, "", 10)
	if err != nil || len(chats) != 1 || chats[0].ID != general.ID {
		t.Fatalf("chats = %+v, %v", chats, err)
	}
	one, err := e.Sessions(ctx, "", "", "", 1)
	if err != nil || len(one) != 1 {
		t.Fatalf("one = %+v, %v", one, err)
	}
	if one[0].ID == sess.ID && general.ID == "" {
		t.Error("the newest session was not first")
	}
}
