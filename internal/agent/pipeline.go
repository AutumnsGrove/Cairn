package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/AutumnsGrove/cairn/internal/decisions"
	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/session"
	"github.com/AutumnsGrove/cairn/internal/store"
)

// preread runs the decisions call before the model sees the message. It never blocks a turn: an unconfigured,
// failing or slow backend becomes one notice and the deterministic parse still runs
// (docs/BUILD.md invariant 13, docs/TESTING.md 5).
func (a *Agent) preread(ctx context.Context, state *RunState) error {
	if a.decisions == nil {
		return a.notice(ctx, state.SessionID, "info", "preread_off",
			"The reading step is not set up. That only changes the hints, not your review.")
	}

	reading := decisions.Reading{Message: state.Text}
	bias := decisions.Session{Active: true, Intent: "log_review"}
	if draft, err := a.session.Draft(ctx, state.SessionID); err == nil {
		bias.Medium = ""
		if work, workErr := a.store.Work(ctx, draft.Entry.WorkID); workErr == nil {
			reading.WorkTitle = work.Title
			bias.Medium = work.Medium
			if state.Medium == "" {
				state.Medium = work.Medium
			}
		}
	} else if !errors.Is(err, session.ErrNoDraft) {
		return err
	}

	start := a.now()
	result, err := a.decisions.Run(ctx, reading, bias)
	latency := int(a.now().Sub(start).Milliseconds())
	if err != nil {
		code := "preread_unavailable"
		if errors.Is(err, decisions.ErrNotConfigured) {
			code = "preread_off"
		}
		return a.notice(ctx, state.SessionID, "warn", code,
			"I could not read this message first. That only changes the hints, not your review.")
	}

	state.PreRead = result.Outcome
	if state.Medium == "" {
		state.Medium = result.Outcome.Medium
	}
	wire := wireOdds(result.Odds)
	if _, appendErr := a.session.Append(ctx, state.SessionID, store.EventPreread, map[string]any{
		"odds": wire, "fired": result.Outcome.Fired, "cost_usd": result.Usage.CostUSD,
	}); appendErr != nil {
		return appendErr
	}

	odds, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("agent: encode odds: %w", err)
	}
	// A ledger write is a receipt, not a gate: if it fails, the review still goes on and the failure is logged.
	if ledgerErr := a.store.RecordDecisionCall(ctx, domain.DecisionCall{
		SessionID: state.SessionID, MessageIndex: state.MessageIndex, Backend: "openrouter", Model: result.Model,
		Odds: odds, Fired: result.Outcome.Fired, CostUSD: result.Usage.CostUSD, LatencyMS: latency,
	}); ledgerErr != nil {
		a.log.Error("decision ledger write failed", "session", state.SessionID, "err", ledgerErr.Error())
	}
	return nil
}

// wireOdds is the probability vector the protocol's preread event carries: one map per check, option to odds.
func wireOdds(in map[string]decisions.Odds) map[string]map[string]float64 {
	out := make(map[string]map[string]float64, len(in))
	for check, odds := range in {
		options := make(map[string]float64, len(odds.Probabilities))
		for option, p := range odds.Probabilities {
			options[option] = p
		}
		out[check] = options
	}
	return out
}

// applyParsed fills the card's empty fields from this message. A later message that says "4.5" or "last night"
// updates the card it belongs to, rather than being read and dropped.
func (a *Agent) applyParsed(ctx context.Context, state *RunState) error {
	if state.Draft == nil {
		return nil
	}
	patch := make(map[string]json.RawMessage, 3)
	if state.Draft.Entry.Rating == 0 {
		if quarters := state.Parsed.RatingQuarters(); quarters != 0 {
			patch["rating"] = mustJSON(quarters)
		}
	}
	if state.Draft.Entry.FinishedOn == "" {
		if date := state.Parsed.DateString(); date != "" {
			patch["finished_on"] = mustJSON(date)
		}
	}
	if state.Draft.Entry.Format == "" {
		if format := state.MediumFormat(); format != "" {
			patch["format"] = mustJSON(format)
		}
	}
	if len(patch) == 0 {
		return nil
	}
	draft, err := a.session.EditDraft(ctx, state.SessionID, session.Edit{
		BaseVersion: state.Draft.Version, Set: patch,
	})
	if err != nil {
		return err
	}
	state.Draft = &draft
	return nil
}

// resolveMedium asks the one question that comes before a lookup: when the pre-read read two real media, she picks
// one rather than the agent guessing (PLAN.md 5, step 4).
func (a *Agent) resolveMedium(ctx context.Context, state *RunState) error {
	if state.Medium != "" || len(state.PreRead.Ambiguous) < 2 {
		return nil
	}
	values := append([]string(nil), state.PreRead.Ambiguous...)
	if a.responder == nil {
		// No one to ask: take the first option the pre-read offered. The model can still change its mind before
		// the lookup.
		state.Medium = values[0]
		return nil
	}
	options := make([]map[string]string, 0, len(values))
	for _, medium := range values {
		options = append(options, map[string]string{"value": medium, "label": mediumLabel(medium)})
	}
	resolution, err := state.ask(ctx, "choose_medium", map[string]any{"options": options})
	if err != nil {
		if errors.Is(err, session.ErrTooManyRounds) {
			state.Medium = values[0]
			return nil
		}
		return err
	}
	choice, err := decodeChoice(resolution)
	if err != nil {
		return nil
	}
	for _, option := range values {
		if strings.EqualFold(choice, option) {
			state.Medium = option
			return nil
		}
	}
	return nil
}

// mediumLabel is the plain words the choose_medium card shows.
func mediumLabel(medium string) string {
	switch medium {
	case "book":
		return "The book"
	case "film":
		return "A film"
	case "tv":
		return "A TV series"
	case "game":
		return "A game"
	case "album":
		return "An album"
	case "podcast":
		return "A podcast"
	case "theatre":
		return "A play"
	default:
		return medium
	}
}

// decodeChoice accepts the two shapes a client answer can take: a bare JSON string, or an object with value or
// choice. Anything else is not an answer, and the caller falls back to the first option.
func decodeChoice(raw json.RawMessage) (string, error) {
	var bare string
	if err := json.Unmarshal(raw, &bare); err == nil && bare != "" {
		return bare, nil
	}
	var object struct {
		Value  string `json:"value"`
		Choice string `json:"choice"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", err
	}
	if object.Value != "" {
		return object.Value, nil
	}
	if object.Choice != "" {
		return object.Choice, nil
	}
	return "", errors.New("agent: the answer names no choice")
}
