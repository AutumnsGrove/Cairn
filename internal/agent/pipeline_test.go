package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/cairn/internal/banks"
	"github.com/AutumnsGrove/cairn/internal/contract"
	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/providers"
	"github.com/AutumnsGrove/cairn/internal/store"
	"github.com/AutumnsGrove/cairn/internal/world"
)

// The whole flow against the fake model: a real session, real lookups over the world's HTTP fakes, real tools, real
// spans. Only the model's replies and the decisions odds are scripted.

const bookMessage = "finished the lantern keeper last night, 4.5/5. the middle dragged a bit but the end stayed with me, honestly"

func toolCall(id, name, args string) world.ChatToolCall {
	return world.ChatToolCall{ID: id, Name: name, Arguments: args}
}

// bookScript is the model's side of a clean book flow.
func bookScript() []world.ChatTurn {
	return []world.ChatTurn{
		{ToolCalls: []world.ChatToolCall{toolCall("c1", "media_lookup", `{"title":"The Lantern Keeper","medium":"book"}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c2", "merge_candidates", `{"candidate_id":"openlibrary|OL1W"}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c3", "ask_reviewer", `{"hints":{"feel":["moving"]}}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c4", "save_entry", `{"body_quotes":["the middle dragged a bit but the end stayed with me"],"verdict_quote":"stayed with me"}`)}},
		{Text: "The card is ready."},
	}
}

// answerQuestions scripts the question form from the same selection the tool will make.
func answerQuestions(t *testing.T, r *rig) {
	t.Helper()
	selection, err := banks.Select(r.agent.banks, banks.Request{
		Medium: "book", CoveredTopics: []string{"story", "pacing"},
	})
	if err != nil {
		t.Fatalf("banks.Select: %v", err)
	}
	if len(selection.Questions) == 0 {
		r.responder.script("question_form", map[string]any{"skip_all": true})
		return
	}
	question := selection.Questions[0]
	value := "the ending landed"
	if len(question.Options) > 0 {
		value = question.Options[0].Value
	}
	r.responder.script("question_form", map[string]any{
		"answers": []map[string]any{{"id": question.ID, "value": value}},
	})
}

func TestPipelineBookFlow(t *testing.T) {
	r := newRig(t, rigOptions{script: bookScript()})
	scriptBook(r.fake)
	r.responder.script("describe", map[string]any{"selected": map[string][]string{"feel": {"moving"}}})
	answerQuestions(t, r)

	ctx := context.Background()
	sess, err := r.session.CreateEntry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.agent.HandleMessage(ctx, TurnInput{
		SessionID: sess.ID, Text: bookMessage, ClientMsgID: "m1",
	})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if out.Ended != "done" {
		t.Errorf("ended = %q", out.Ended)
	}
	if out.Steps != 4 {
		t.Errorf("steps = %d, want 4", out.Steps)
	}
	if out.Draft == nil {
		t.Fatal("no draft card")
	}
	entry := out.Draft.Entry
	if entry.Status != domain.StatusDraft {
		t.Errorf("status = %q; a card is a draft until she confirms", entry.Status)
	}
	if entry.Rating != 18 {
		t.Errorf("rating = %d, want 18 (4.5 stars)", entry.Rating)
	}
	if entry.FinishedOn != "2026-11-01" {
		t.Errorf("finished_on = %q", entry.FinishedOn)
	}
	if entry.Body != "the middle dragged a bit but the end stayed with me" {
		t.Errorf("body = %q", entry.Body)
	}
	if entry.BodyOrigin != domain.BodyExtracted {
		t.Errorf("origin = %q", entry.BodyOrigin)
	}
	if entry.Verdict != "stayed with me" {
		t.Errorf("verdict = %q", entry.Verdict)
	}
	if len(entry.BodySpans) != 1 || !strings.Contains(bookMessage, entry.BodySpans[0].Text) {
		t.Errorf("spans = %+v", entry.BodySpans)
	}
	if entry.WorkID == "" {
		t.Error("the card has no work")
	}
	work, err := r.store.Work(ctx, entry.WorkID)
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if work.Title != "The Lantern Keeper" || work.Medium != "book" || !work.Verified {
		t.Errorf("work = %+v", work)
	}
	sources, err := r.store.Sources(ctx, work.ID)
	if err != nil || len(sources) != 1 || sources[0].Provider != "openlibrary" || sources[0].Trust != domain.TrustVerified {
		t.Errorf("sources = %+v, %v", sources, err)
	}

	// The answer she gave is stored with its prompt, and the chip answer is stored by its stable value.
	answers, err := r.store.Answers(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundChip, foundQuestion := false, false
	for _, answer := range answers {
		if answer.QuestionID == "feel" && len(answer.Values) == 1 && answer.Values[0] == "moving" {
			foundChip = true
		}
		if answer.QuestionID != "" && answer.Type != domain.AnswerTags {
			foundQuestion = true
		}
	}
	if !foundChip || !foundQuestion {
		t.Errorf("answers = %+v", answers)
	}

	// Both ledgers have a row, and the turn cost is real.
	turns, err := r.store.TurnCalls(ctx, sess.ID)
	if err != nil || len(turns) != 1 {
		t.Fatalf("turn ledger = %+v, %v", turns, err)
	}
	if turns[0].PromptTokens == 0 || out.CostUSD == 0 {
		t.Errorf("turn = %+v, outcome cost = %v", turns[0], out.CostUSD)
	}
	if total, costErr := r.store.SessionCost(ctx, sess.ID); costErr != nil || total < out.CostUSD {
		t.Errorf("session cost = %v, %v", total, costErr)
	}

	// The persisted event log carries the whole flow in order.
	kinds := map[string]int{}
	events, err := r.store.EventsSince(ctx, sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		kinds[ev.Kind]++
	}
	for _, want := range []string{
		store.EventUserMessage, store.EventPreread, store.EventDraftSnapshot,
		store.EventToolStart, store.EventToolEnd, store.EventInteractionRequest, store.EventInteractionResolved,
		store.EventAssistantStart, store.EventTextDelta, store.EventAssistantEnd,
	} {
		if kinds[want] == 0 {
			t.Errorf("the log has no %s event", want)
		}
	}
	if kinds[store.EventToolStart] != 4 || kinds[store.EventToolEnd] != 4 {
		t.Errorf("tool events = %d starts, %d ends", kinds[store.EventToolStart], kinds[store.EventToolEnd])
	}
	var interactions []string
	for _, ev := range events {
		if ev.Kind != store.EventInteractionRequest {
			continue
		}
		var payload struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(ev.Payload, &payload) == nil {
			interactions = append(interactions, payload.Kind)
		}
	}
	if !strings.Contains(strings.Join(interactions, ","), "choose_candidate") {
		t.Errorf("she never got the candidate card: interactions = %v", interactions)
	}
	if !strings.Contains(strings.Join(interactions, ","), "describe") {
		t.Errorf("she never got the Describe step: interactions = %v", interactions)
	}
	// Nothing committed the entry: the confirm tap is the only commit.
	saved, _, err := r.store.Library(ctx, domain.Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 0 {
		t.Errorf("a non-draft entry reached the library: %+v", saved)
	}
}

// Bad: a model that invents her words is refused, and a later honest quote is kept.
func TestPipelineRefusesAnInventedBody(t *testing.T) {
	script := []world.ChatTurn{
		{ToolCalls: []world.ChatToolCall{toolCall("c1", "media_lookup", `{"title":"The Lantern Keeper","medium":"book"}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c2", "merge_candidates", `{"candidate_id":"openlibrary|OL1W"}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c3", "save_entry", `{"body_quotes":["a paragraph she never wrote"]}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c4", "save_entry", `{"body_quotes":["the middle dragged a bit"],"verdict_quote":"invented verdict"}`)}},
		{Text: "The card is ready."},
	}
	r := newRig(t, rigOptions{script: script})
	scriptBook(r.fake)
	ctx := context.Background()
	sess, _ := r.session.CreateEntry(ctx)
	out, err := r.agent.HandleMessage(ctx, TurnInput{SessionID: sess.ID, Text: bookMessage, ClientMsgID: "m1"})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if out.Draft == nil {
		t.Fatal("no card")
	}
	if out.Draft.Entry.Body != "the middle dragged a bit" {
		t.Errorf("body = %q, want only her words", out.Draft.Entry.Body)
	}
	if out.Draft.Entry.Verdict != "" {
		t.Errorf("verdict = %q, want empty for an invented quote", out.Draft.Entry.Verdict)
	}
}

// Bad: a model that invents a body and never recovers leaves the card without one, and the guard still holds.
func TestPipelineNeverStoresAnInventedBody(t *testing.T) {
	script := []world.ChatTurn{
		{ToolCalls: []world.ChatToolCall{toolCall("c1", "media_lookup", `{"title":"The Lantern Keeper","medium":"book"}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c2", "merge_candidates", `{"candidate_id":"openlibrary|OL1W"}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c3", "save_entry", `{"body_quotes":["a paragraph she never wrote"]}`)}},
		{Text: "I could not find a quote."},
	}
	r := newRig(t, rigOptions{script: script})
	scriptBook(r.fake)
	ctx := context.Background()
	sess, _ := r.session.CreateEntry(ctx)
	out, err := r.agent.HandleMessage(ctx, TurnInput{SessionID: sess.ID, Text: bookMessage, ClientMsgID: "m1"})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if out.Draft == nil {
		t.Fatal("no card")
	}
	if out.Draft.Entry.Body != "" || out.Draft.Entry.BodyOrigin != domain.BodyTyped {
		t.Errorf("an invented body reached the card: %+v", out.Draft.Entry)
	}
}

// Sad: a message with no rating asks for one, and the value she gives is the value stored.
func TestPipelineAsksForAMissingRating(t *testing.T) {
	script := []world.ChatTurn{
		{ToolCalls: []world.ChatToolCall{toolCall("c1", "media_lookup", `{"title":"The Lantern Keeper","medium":"book"}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c2", "merge_candidates", `{"candidate_id":"openlibrary|OL1W"}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c3", "save_entry", `{"body_quotes":["the middle dragged a bit but the end stayed with me"]}`)}},
		{Text: "The card is ready."},
	}
	r := newRig(t, rigOptions{script: script})
	scriptBook(r.fake)
	r.responder.script("fill_gap", "4/5")
	ctx := context.Background()
	sess, _ := r.session.CreateEntry(ctx)
	out, err := r.agent.HandleMessage(ctx, TurnInput{
		SessionID: sess.ID, Text: "finished the lantern keeper last night. the middle dragged a bit but the end stayed with me", ClientMsgID: "m1",
	})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if out.Draft == nil || out.Draft.Entry.Rating != 16 {
		t.Fatalf("draft = %+v, want a rating of 16 (4 stars)", out.Draft)
	}
}

// Sad: a provider outage opens the one lookup_failed card, and logging without details still makes a card.
func TestPipelineLogsWithoutDetailsWhenTheProviderIsDown(t *testing.T) {
	r := newRig(t, rigOptions{script: []world.ChatTurn{
		{ToolCalls: []world.ChatToolCall{toolCall("c1", "media_lookup", `{"title":"The Lantern Keeper","medium":"book"}`)}},
		{ToolCalls: []world.ChatToolCall{toolCall("c2", "save_entry", `{"body_quotes":["the middle dragged a bit but the end stayed with me"]}`)}},
		{Text: "Logged without details."},
	}})
	scriptBook(r.fake)
	r.ol.SetFault("/search.json", world.FaultError)
	r.responder.script("lookup_failed", "log_without_details")
	ctx := context.Background()
	sess, _ := r.session.CreateEntry(ctx)
	out, err := r.agent.HandleMessage(ctx, TurnInput{
		SessionID: sess.ID, Text: "finished the lantern keeper last night, 4.5/5. the middle dragged a bit but the end stayed with me", ClientMsgID: "m1",
	})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if out.Draft == nil {
		t.Fatal("no card after logging without details")
	}
	work, err := r.store.Work(ctx, out.Draft.Entry.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if work.Verified || work.Title != "The Lantern Keeper" {
		t.Errorf("manual work = %+v", work)
	}
	// The outage is sticky: a second lookup still fails and still degrades, rather than clearing on a retry.
	if _, err := r.finder.Lookup(ctx, providers.Query{Medium: "book", Title: "The Lantern Keeper"}); err == nil {
		t.Error("the provider fault cleared on the second call")
	}
}

// The events the agent persists are what S09 will put on the wire, so they are checked against the contract now,
// not after the socket exists. The draft snapshot is the one shape S09 maps; it is not the card's wire form yet.
func TestPersistedEventsMatchTheContract(t *testing.T) {
	r := newRig(t, rigOptions{script: bookScript()})
	scriptBook(r.fake)
	r.responder.script("describe", map[string]any{"selected": map[string][]string{}})
	r.responder.script("question_form", map[string]any{"skip_all": true})
	ctx := context.Background()
	sess, _ := r.session.CreateEntry(ctx)
	if _, err := r.agent.HandleMessage(ctx, TurnInput{SessionID: sess.ID, Text: bookMessage, ClientMsgID: "m1"}); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	validator, err := contract.Default()
	if err != nil {
		t.Fatalf("contract.Default: %v", err)
	}
	events, err := r.store.EventsSince(ctx, sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, ev := range events {
		switch ev.Kind {
		case store.EventDraftSnapshot, store.EventSaved, store.EventDiscarded:
			continue
		}
		raw, err := json.Marshal(map[string]any{
			"seq": ev.Seq, "type": ev.Kind, "session_id": ev.SessionID,
			"ts": ev.CreatedAt.UTC().Format(time.RFC3339), "data": ev.Payload,
		})
		if err != nil {
			t.Fatalf("encode %s: %v", ev.Kind, err)
		}
		if err := validator.ValidateServerEvent(raw); err != nil {
			t.Errorf("%s does not match the contract: %v\n%s", ev.Kind, err, raw)
		}
		checked++
	}
	if checked < 10 {
		t.Errorf("checked %d events, want the whole flow", checked)
	}
}
