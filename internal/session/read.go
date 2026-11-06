package session

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/AutumnsGrove/sepia/internal/domain"
	"github.com/AutumnsGrove/sepia/internal/store"
)

// The engine is also the app's read model. docs/BUILD.md 4 lets the server import session and not the store, so
// the pages that show a saved review are read here, beside the rules that say what an entry is. Every read is
// bounded by the caller's limit; nothing here returns an unbounded list.

// Card is one draft and everything the card shows around it: the work, the sources behind it, her answers, the
// passages her body left out, and the "why" sheet's receipts.
type Card struct {
	Draft      Draft
	Work       domain.Work
	Sources    []domain.Source
	Answers    []domain.Answer
	Tags       []domain.Tag
	SourceText string
	LeftOut    []domain.Segment

	Checks      []Check
	Tools       []ToolTrace
	PrereadCost float64
	TurnCost    float64
	Fidelity    string
}

// Check is one line of the "why" sheet: the winning option of a decisions check and how sure the model was.
type Check struct {
	Name        string
	Option      string
	Probability float64
	Fired       bool
}

// ToolTrace is one tool call the turn made, as the "why" sheet shows it.
type ToolTrace struct {
	Tool    string
	OK      bool
	Summary string
	Sources []ToolSource
}

// ToolSource is one citation behind a tool call.
type ToolSource struct {
	URL   string
	Title string
}

// Card reads the entry card for a session. It is the read the socket and the REST handlers both need, so the
// shape of a draft cannot differ between them.
func (e *Engine) Card(ctx context.Context, sessionID string) (Card, error) {
	draft, err := e.Draft(ctx, sessionID)
	if err != nil {
		return Card{}, err
	}
	return e.CardFor(ctx, sessionID, draft.Entry, draft.Version)
}

// CardFor builds a card for a version of the entry that is not necessarily the current one. The socket replays a
// draft_snapshot from the log, and a client must see the card as it was at that moment, not the one it was later
// edited into.
func (e *Engine) CardFor(ctx context.Context, sessionID string, entry domain.Entry, version int) (Card, error) {
	card := Card{Draft: Draft{Version: version, Entry: entry}}
	open, err := e.store.OpenInteractions(ctx, sessionID)
	if err != nil {
		return Card{}, err
	}
	for _, question := range open {
		card.Draft.Open = append(card.Draft.Open, OpenQuestion{ID: question.ID, Kind: question.Kind})
	}
	if entry.WorkID != "" {
		if work, workErr := e.store.Work(ctx, entry.WorkID); workErr == nil {
			card.Work = work
		}
		if sources, sourceErr := e.store.Sources(ctx, entry.WorkID); sourceErr == nil {
			card.Sources = sources
		}
	}
	answers, err := e.store.Answers(ctx, entry.ID)
	if err != nil {
		return Card{}, err
	}
	card.Answers = answers
	if entry.ID != "" {
		if tags, tagErr := e.store.EntryTags(ctx, entry.ID); tagErr == nil {
			card.Tags = tags
		}
	}
	e.receipts(ctx, sessionID, &card)
	sourceText, err := e.SourceText(ctx, sessionID)
	if err != nil {
		return Card{}, err
	}
	card.SourceText = sourceText
	// The stored spans are in source order; Complement trusts that order and returns nothing if a span is
	// damaged, which is the safe answer for a card that is only being shown.
	spans := append([]domain.Span(nil), entry.BodySpans...)
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	card.LeftOut = domain.Complement(sourceText, spans)
	return card, nil
}

// EntryView is a saved entry with the work it is about and the answers she gave.
type EntryView struct {
	Entry   domain.Entry
	Work    domain.Work
	Answers []domain.Answer
	Tags    []domain.Tag
}

// EntryView reads one saved or draft entry for a page. A missing work is not an error: a library row is still
// readable if its work row was lost, and the page shows the id rather than failing.
func (e *Engine) EntryView(ctx context.Context, entryID string) (EntryView, error) {
	entry, err := e.store.Entry(ctx, entryID)
	if err != nil {
		return EntryView{}, err
	}
	view := EntryView{Entry: entry}
	if entry.WorkID != "" {
		if work, workErr := e.store.Work(ctx, entry.WorkID); workErr == nil {
			view.Work = work
		}
	}
	answers, err := e.store.Answers(ctx, entryID)
	if err != nil {
		return EntryView{}, err
	}
	view.Answers = answers
	if tags, tagErr := e.store.EntryTags(ctx, entryID); tagErr == nil {
		view.Tags = tags
	}
	return view, nil
}

// WorkView is one work and every source that contributed to it.
type WorkView struct {
	Work    domain.Work
	Sources []domain.Source
}

// WorkView reads one work for the detail page.
func (e *Engine) WorkView(ctx context.Context, workID string) (WorkView, error) {
	work, err := e.store.Work(ctx, workID)
	if err != nil {
		return WorkView{}, err
	}
	sources, err := e.store.Sources(ctx, workID)
	if err != nil {
		return WorkView{}, err
	}
	return WorkView{Work: work, Sources: sources}, nil
}

// Library is one page of saved entries, newest finished first, with the cursor for the next page. favourite is nil
// when the list is not filtered by it.
func (e *Engine) Library(ctx context.Context, medium string, favourite *bool, cursor domain.Cursor,
	limit int,
) ([]domain.Entry, domain.Cursor, error) {
	return e.store.LibraryPage(ctx, medium, favourite, cursor, limit)
}

// Sessions is one page of sessions, newest first. before is the opaque cursor from the previous page.
func (e *Engine) Sessions(ctx context.Context, state domain.SessionState, kind domain.SessionKind, before string,
	limit int,
) ([]domain.Session, error) {
	return e.store.Sessions(ctx, state, kind, before, limit)
}

// RevisionsPage is one page of an entry's append-only history, newest first.
func (e *Engine) RevisionsPage(ctx context.Context, entryID string, before, limit int) ([]domain.Revision, int, error) {
	return e.store.RevisionsPage(ctx, entryID, before, limit)
}

// DeleteEntry soft-deletes one entry. There is no hard delete and no purge (docs/BUILD.md invariant 4).
func (e *Engine) DeleteEntry(ctx context.Context, entryID string) error {
	return e.store.SoftDeleteEntry(ctx, entryID)
}

// EventsSince replays the session log after a client's last seen sequence number. The socket's tail uses it to
// follow a turn without depending on the writer.
func (e *Engine) EventsSince(ctx context.Context, sessionID string, seq int) ([]domain.SessionEvent, error) {
	return e.store.EventsSince(ctx, sessionID, seq)
}

// HeadSequence is the highest sequence number in a session.
func (e *Engine) HeadSequence(ctx context.Context, sessionID string) (int, error) {
	return e.store.HeadSequence(ctx, sessionID)
}

// SessionCost is the running total a session has spent, which the cost_update event carries.
func (e *Engine) SessionCost(ctx context.Context, sessionID string) (float64, error) {
	return e.store.SessionCost(ctx, sessionID)
}

// IsNotFound reports whether an engine error means "there is no such row". The server imports session and not the
// store (docs/BUILD.md 4), so the sentinels it branches on are re-exported here.
func IsNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// IsDuplicate reports whether a client message was already applied.
func IsDuplicate(err error) bool { return errors.Is(err, store.ErrDuplicateClientMessage) }

// receipts fills the "why" sheet from the two ledgers and the tool events. A read failure is not fatal: a sheet
// with a missing number is still readable, and the card must never fail because a receipt did.
func (e *Engine) receipts(ctx context.Context, sessionID string, card *Card) {
	calls, err := e.store.DecisionCalls(ctx, sessionID)
	if err == nil {
		for _, call := range calls {
			card.PrereadCost += call.CostUSD
			var odds map[string]map[string]float64
			if json.Unmarshal(call.Odds, &odds) != nil {
				continue
			}
			for name, options := range odds {
				option, probability := winningOption(options)
				if option == "" {
					continue
				}
				fired := containsString(call.Fired, name) || (name == "extract_fidelity" && len(call.Fired) > 0)
				card.Checks = append(card.Checks, Check{
					Name: name, Option: option, Probability: probability, Fired: fired,
				})
				if name == "extract_fidelity" {
					card.Fidelity = fidelityOf(call.Fired, option)
				}
			}
			if len(card.Checks) >= 25 {
				card.Checks = card.Checks[:25]
				break
			}
		}
	}
	if turns, turnErr := e.store.TurnCalls(ctx, sessionID); turnErr == nil {
		for _, turn := range turns {
			card.TurnCost += turn.CostUSD
		}
	}
	events, err := e.store.EventsSince(ctx, sessionID, 0)
	if err != nil {
		return
	}
	for _, event := range events {
		if event.Kind != "tool_end" {
			continue
		}
		var payload struct {
			Tool    string `json:"tool"`
			OK      bool   `json:"ok"`
			Summary string `json:"summary"`
			Sources []struct {
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"sources"`
		}
		if json.Unmarshal(event.Payload, &payload) != nil || payload.Tool == "" {
			continue
		}
		trace := ToolTrace{Tool: payload.Tool, OK: payload.OK, Summary: payload.Summary}
		for _, source := range payload.Sources {
			trace.Sources = append(trace.Sources, ToolSource{URL: source.URL, Title: source.Title})
		}
		card.Tools = append(card.Tools, trace)
		if len(card.Tools) == 12 {
			break
		}
	}
}

// winningOption is the option with the highest odds, which is the one the sheet shows. A tie is broken by name so
// the sheet is stable across runs.
func winningOption(options map[string]float64) (string, float64) {
	best, bestProbability := "", -1.0
	for option, probability := range options {
		if probability > bestProbability || (probability == bestProbability && option < best) {
			best, bestProbability = option, probability
		}
	}
	if bestProbability < 0 {
		return "", 0
	}
	return best, bestProbability
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// fidelityOf maps the fidelity call to the card's status. The fired list holds the verdict; the winning option is
// the fallback if a future call leaves it empty.
func fidelityOf(fired []string, option string) string {
	for _, value := range fired {
		switch value {
		case "faithful", "drops_opinion", "adds_content":
			return value
		}
	}
	switch option {
	case "faithful", "drops_opinion", "adds_content":
		return option
	default:
		return "unknown"
	}
}
