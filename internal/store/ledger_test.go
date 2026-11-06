package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/AutumnsGrove/sepia/internal/domain"
)

// The two ledgers are the answer to "why did this turn cost that". They hold odds, counts and money, never text.

func TestLedgerWritesBothKindsAndSums(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const at = "2026-11-02T10:00:00Z"
	if err := raw(t, s, `INSERT INTO sessions (id, kind, created_at, updated_at) VALUES ('sess1', 'entry', ?, ?)`, at, at); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordDecisionCall(ctx, domain.DecisionCall{
		SessionID: "sess1", MessageIndex: 1, Backend: "openrouter", Model: "jev-1.14",
		Odds: json.RawMessage(`{"intent":{"log_review":0.93}}`), Fired: []string{"intent"},
		CostUSD: 0.0002, LatencyMS: 40,
	}); err != nil {
		t.Fatalf("RecordDecisionCall: %v", err)
	}
	if err := s.RecordTurn(ctx, domain.TurnCall{
		SessionID: "sess1", TurnIndex: 1, Model: "vendor/model",
		PromptTokens: 1200, CompletionTokens: 80, CostUSD: 0.0022, LatencyMS: 900, Reason: "done",
	}); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}

	total, err := s.SessionCost(ctx, "sess1")
	if err != nil {
		t.Fatalf("SessionCost: %v", err)
	}
	if total < 0.00239 || total > 0.00241 {
		t.Errorf("total = %v, want 0.0024", total)
	}

	turns, err := s.TurnCalls(ctx, "sess1")
	if err != nil {
		t.Fatalf("TurnCalls: %v", err)
	}
	if len(turns) != 1 || turns[0].PromptTokens != 1200 || turns[0].Reason != "done" {
		t.Errorf("turns = %+v", turns)
	}

	// A session with no calls is zero, not an error.
	if empty, err := s.SessionCost(ctx, "sess-none"); err != nil || empty != 0 {
		t.Errorf("empty session cost = %v, %v", empty, err)
	}
}

// A turn may be recorded without a session, which is what a startup check or a test does. The column is nullable,
// so an empty id becomes NULL rather than a dangling reference.
func TestLedgerAllowsATurnWithNoSession(t *testing.T) {
	s := openTest(t)
	if err := s.RecordTurn(context.Background(), domain.TurnCall{TurnIndex: 0, Model: "vendor/model"}); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}
}
