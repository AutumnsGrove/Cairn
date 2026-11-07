package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"

	"github.com/AutumnsGrove/sepia/internal/banks"
	"github.com/AutumnsGrove/sepia/internal/contract"
	"github.com/AutumnsGrove/sepia/internal/domain"
	"github.com/AutumnsGrove/sepia/internal/providers"
	"github.com/AutumnsGrove/sepia/internal/store"
	"github.com/AutumnsGrove/sepia/internal/world"
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
	if total, costErr := r.store.SessionCost(ctx, sess.ID); costErr != nil || total.Total < out.CostUSD {
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

// A turn that takes two model runs reports both, and nova's own sum decides how the two costs combine. Sepia used to
// add the fields up by hand, which dropped everything nova added in v0.3 (allowance and list-price dollars, billing
// source) and let a weaker cost provenance replace a stronger one.
func TestTwoModelRunsReportTheWholeCost(t *testing.T) {
	script := []world.ChatTurn{
		{
			ToolCalls: []world.ChatToolCall{toolCall("c1", "search_library", `{"query":"lantern"}`)},
			Usage: provider.Usage{
				PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110,
				CostUSD: 0.0001, CostSource: "provider",
			},
		},
		{
			Text: "Nothing in the library yet.",
			Usage: provider.Usage{
				PromptTokens: 200, CompletionTokens: 20, TotalTokens: 220,
				CostUSD: 0.0002, CostSource: "allowance", AllowanceUSD: 0.0002,
			},
		},
	}
	var mu sync.Mutex
	var live []Event
	r := newRig(t, rigOptions{script: script, live: func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		live = append(live, ev)
	}})
	ctx := context.Background()
	sess, err := r.session.CreateGeneral(ctx, "what did I think of the lantern keeper?")
	if err != nil {
		t.Fatalf("CreateGeneral: %v", err)
	}
	out, err := r.agent.HandleMessage(ctx, TurnInput{
		SessionID: sess.ID, Text: "have I written about the lantern keeper?", ClientMsgID: "m1",
	})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	// Dollars add as floats, so the sum is compared to within a hundredth of a cent.
	if out.CostUSD < 0.000299 || out.CostUSD > 0.000301 {
		t.Errorf("outcome cost = %v, want both runs summed (0.0003)", out.CostUSD)
	}
	if out.PromptTokens != 300 || out.CompletionTokens != 30 {
		t.Errorf("tokens = %d/%d, want 300/30", out.PromptTokens, out.CompletionTokens)
	}
	turns, err := r.store.TurnCalls(ctx, sess.ID)
	if err != nil || len(turns) != 1 {
		t.Fatalf("turn ledger = %+v, %v", turns, err)
	}
	if turns[0].CostUSD < 0.000299 || turns[0].CostUSD > 0.000301 {
		t.Errorf("the ledger recorded %v, want the summed 0.0003", turns[0].CostUSD)
	}
	// OpenRouter reports real money and has no allowance concept, so the receipt says provider and nothing covered.
	// The covered case is a plan backend, and TestAnAllowanceCoveredTurnIsRecordedAsCovered drives that one.
	if turns[0].CostSource != domain.CostSourceProvider || turns[0].AllowanceUSD != 0 {
		t.Errorf("provenance = %q, %v, want provider with nothing covered", turns[0].CostSource, turns[0].AllowanceUSD)
	}

	// The live total a phone shows carries the split too, never one ambiguous number.
	mu.Lock()
	defer mu.Unlock()
	last := lastCostUpdate(t, live)
	sessionCost, err := r.store.SessionCost(ctx, sess.ID)
	if err != nil {
		t.Fatalf("SessionCost: %v", err)
	}
	if last["session_total_usd"] != sessionCost.Total || last["session_covered_usd"] != sessionCost.Covered {
		t.Errorf("cost_update = %v, want the ledgers' total %v covered %v",
			last, sessionCost.Total, sessionCost.Covered)
	}
}

// lastCostUpdate is the newest live cost event, which is the one a phone is showing.
func lastCostUpdate(t *testing.T, live []Event) map[string]any {
	t.Helper()
	for i := len(live) - 1; i >= 0; i-- {
		if live[i].Kind != "cost_update" {
			continue
		}
		data, ok := live[i].Data.(map[string]any)
		if !ok {
			t.Fatalf("cost_update data is %T, want a map", live[i].Data)
		}
		return data
	}
	t.Fatal("no cost_update event")
	return nil
}

// allowanceProvider is a plan backend: it reports what the call consumed from an allowance rather than money.
type allowanceProvider struct{ covered float64 }

func (allowanceProvider) Name() string { return "allowance" }

func (p allowanceProvider) Stream(ctx context.Context, _ provider.Request,
	emit func(provider.StreamEvent),
) (*provider.Response, error) {
	emit(provider.StreamEvent{Type: provider.StreamTextDelta, Text: "Noted."})
	return &provider.Response{
		Message:      message.Assistant("Noted."),
		FinishReason: "stop",
		Usage: provider.Usage{
			PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110,
			CostUSD: p.covered, AllowanceUSD: p.covered, CostSource: domain.CostSourceAllowance,
		},
	}, nil
}

// A turn a plan's allowance covered is recorded as covered, in the ledger and in the running total. The number is
// the same one the provider consumed; what changes is whose dollars they were (docs/BUILD-LOG.md 2026-11-07).
func TestAnAllowanceCoveredTurnIsRecordedAsCovered(t *testing.T) {
	var mu sync.Mutex
	var live []Event
	r := newRig(t, rigOptions{
		providerOverride: allowanceProvider{covered: 0.002},
		live: func(ev Event) {
			mu.Lock()
			defer mu.Unlock()
			live = append(live, ev)
		},
	})
	ctx := context.Background()
	sess, err := r.session.CreateGeneral(ctx, "a plan-covered question")
	if err != nil {
		t.Fatalf("CreateGeneral: %v", err)
	}
	if _, turnErr := r.agent.HandleMessage(ctx, TurnInput{
		SessionID: sess.ID, Text: "just checking in", ClientMsgID: "m1",
	}); turnErr != nil {
		t.Fatalf("HandleMessage: %v", turnErr)
	}

	turns, err := r.store.TurnCalls(ctx, sess.ID)
	if err != nil || len(turns) != 1 {
		t.Fatalf("turn ledger = %+v, %v", turns, err)
	}
	if turns[0].CostSource != domain.CostSourceAllowance {
		t.Errorf("cost source = %q, want allowance", turns[0].CostSource)
	}
	if turns[0].CostUSD != 0.002 || turns[0].AllowanceUSD != 0.002 {
		t.Errorf("cost = %v covered %v, want the allowance 0.002 both", turns[0].CostUSD, turns[0].AllowanceUSD)
	}
	if spent := turns[0].Cost().Spent(); spent != 0 {
		t.Errorf("spent = %v, want nothing spent", spent)
	}

	mu.Lock()
	defer mu.Unlock()
	last := lastCostUpdate(t, live)
	if last["session_covered_usd"].(float64) <= 0 {
		t.Errorf("cost_update = %v, want a covered part", last)
	}
	if last["session_covered_usd"].(float64) > last["session_total_usd"].(float64) {
		t.Errorf("cost_update = %v, want covered inside the total", last)
	}
}
