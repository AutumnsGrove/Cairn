package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/store/db"
)

// The cost ledgers. Both are append-only records of what a turn spent: a decisions pre-read in decision_calls, a
// chat turn in turn_calls. Neither holds text, so a ledger can be read while debugging without becoming a second
// copy of a review (docs/BUILD.md invariant 5).

// RecordDecisionCall writes one decisions call. An empty id or timestamp is filled in, so the caller does not have
// to know the store's clock and id source.
func (s *Store) RecordDecisionCall(ctx context.Context, call domain.DecisionCall) error {
	if call.ID == "" {
		call.ID = s.NewID()
	}
	if call.CreatedAt.IsZero() {
		call.CreatedAt = s.Now()
	}
	odds := call.Odds
	if len(odds) == 0 {
		odds = json.RawMessage("{}")
	}
	if !json.Valid(odds) {
		return fmt.Errorf("store: decision odds are not JSON")
	}
	fired, err := json.Marshal(call.Fired)
	if err != nil {
		return fmt.Errorf("store: encode fired checks: %w", err)
	}
	if call.Fired == nil {
		fired = []byte("[]")
	}
	return s.InTx(ctx, func(q *db.Queries) error {
		return q.CreateDecisionCall(ctx, db.CreateDecisionCallParams{
			ID: call.ID, SessionID: nullableString(call.SessionID), MessageIndex: int64(call.MessageIndex),
			Backend: call.Backend, Model: call.Model, OddsJson: string(odds), FiredJson: string(fired),
			CostUsd: call.CostUSD, LatencyMs: int64(call.LatencyMS),
			CreatedAt: call.CreatedAt.UTC().Format(timeLayout),
		})
	})
}

// RecordTurn writes one model turn to the turn ledger.
func (s *Store) RecordTurn(ctx context.Context, turn domain.TurnCall) error {
	if turn.ID == "" {
		turn.ID = s.NewID()
	}
	if turn.CreatedAt.IsZero() {
		turn.CreatedAt = s.Now()
	}
	return s.InTx(ctx, func(q *db.Queries) error {
		return q.CreateTurnCall(ctx, db.CreateTurnCallParams{
			ID: turn.ID, SessionID: nullableString(turn.SessionID), TurnIndex: int64(turn.TurnIndex),
			Model: turn.Model, PromptTokens: int64(turn.PromptTokens), CompletionTokens: int64(turn.CompletionTokens),
			CostUsd: turn.CostUSD, LatencyMs: int64(turn.LatencyMS), Reason: turn.Reason,
			CreatedAt: turn.CreatedAt.UTC().Format(timeLayout),
		})
	})
}

// TurnCalls reads a session's turn ledger, oldest first. It is for the "why" sheet and for tests.
func (s *Store) TurnCalls(ctx context.Context, sessionID string) ([]domain.TurnCall, error) {
	rows, err := s.ReadQueries().ListTurnCalls(ctx, nullableString(sessionID))
	if err != nil {
		return nil, fmt.Errorf("store: list turn calls: %w", err)
	}
	out := make([]domain.TurnCall, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.TurnCall{
			ID: row.ID, SessionID: derefString(row.SessionID), TurnIndex: int(row.TurnIndex),
			Model: row.Model, PromptTokens: int(row.PromptTokens), CompletionTokens: int(row.CompletionTokens),
			CostUSD: row.CostUsd, LatencyMS: int(row.LatencyMs), Reason: row.Reason,
			CreatedAt: parseTime(row.CreatedAt),
		})
	}
	return out, nil
}

// SessionCost is the running total for a session, read from both ledgers rather than a counter that can drift.
func (s *Store) SessionCost(ctx context.Context, sessionID string) (float64, error) {
	total, err := s.ReadQueries().SessionCost(ctx, db.SessionCostParams{
		SessionID: nullableString(sessionID), SessionID_2: &sessionID,
	})
	if err != nil {
		return 0, fmt.Errorf("store: session cost: %w", err)
	}
	return total, nil
}

// parseTime reads a stored RFC 3339 instant. A malformed value reads as the zero time rather than failing a list:
// the ledger is a receipt, and a receipt with a bad date is still readable.
func parseTime(value string) time.Time {
	t, err := time.Parse(timeLayout, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
