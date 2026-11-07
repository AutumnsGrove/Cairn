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
	if total.Total < 0.00239 || total.Total > 0.00241 {
		t.Errorf("total = %v, want 0.0024", total.Total)
	}

	turns, err := s.TurnCalls(ctx, "sess1")
	if err != nil {
		t.Fatalf("TurnCalls: %v", err)
	}
	if len(turns) != 1 || turns[0].PromptTokens != 1200 || turns[0].Reason != "done" {
		t.Errorf("turns = %+v", turns)
	}

	// A session with no calls is zero, not an error.
	if empty, err := s.SessionCost(ctx, "sess-none"); err != nil || empty.Total != 0 || empty.Covered != 0 {
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

// The "why" sheet reads the decisions ledger back. It is a receipt, so a row read back must say the same thing it
// said when it was written, and a row that is not JSON must never be written at all.
func TestDecisionCallsAreReadBackAndRefuseBadJSON(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const at = "2026-11-02T10:00:00Z"
	if err := raw(t, s, `INSERT INTO sessions (id, kind, created_at, updated_at) VALUES ('sess1', 'entry', ?, ?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDecisionCall(ctx, domain.DecisionCall{
		SessionID: "sess1", MessageIndex: 1, Backend: "openrouter", Model: "jev-1.14",
		Odds: json.RawMessage(`{"intent":{"log_review":0.93}}`), Fired: []string{"intent"},
		CostUSD: 0.0002, CostSource: domain.CostSourceAllowance, AllowanceUSD: 0.0002, LatencyMS: 40,
	}); err != nil {
		t.Fatalf("RecordDecisionCall: %v", err)
	}

	calls, err := s.DecisionCalls(ctx, "sess1")
	if err != nil {
		t.Fatalf("DecisionCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	call := calls[0]
	if call.ID == "" || call.CreatedAt.IsZero() {
		t.Errorf("the store left its own id and clock empty: %+v", call)
	}
	if call.Backend != "openrouter" || call.MessageIndex != 1 || len(call.Fired) != 1 || call.Fired[0] != "intent" {
		t.Errorf("call = %+v", call)
	}
	if string(call.Odds) != `{"intent":{"log_review":0.93}}` {
		t.Errorf("odds = %s", call.Odds)
	}
	// The row remembers whose dollars it was, so the "why" sheet can say "covered" instead of "spent".
	if call.CostSource != domain.CostSourceAllowance || call.AllowanceUSD != 0.0002 {
		t.Errorf("provenance = %q, %v", call.CostSource, call.AllowanceUSD)
	}
	if cost := call.Cost(); cost.Spent() != 0 || cost.Estimated {
		t.Errorf("cost = %+v, want all of it covered", cost)
	}
	if calls, err := s.DecisionCalls(ctx, "sess-none"); err != nil || len(calls) != 0 {
		t.Errorf("calls for a session that asked nothing = %+v, %v", calls, err)
	}

	// Odds that are not JSON are a bug in the caller, so they are refused rather than stored as a broken receipt.
	if err := s.RecordDecisionCall(ctx, domain.DecisionCall{SessionID: "sess1", Odds: json.RawMessage(`{`)}); err == nil {
		t.Fatal("malformed odds were stored")
	}
}

// A receipt with a bad date is still readable. The ledger is append-only, so a row written by an older build must
// not make the "why" sheet fail to open.
func TestAMalformedLedgerTimestampReadsAsTheZeroTime(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const at = "2026-11-02T10:00:00Z"
	if err := raw(t, s, `INSERT INTO sessions (id, kind, created_at, updated_at) VALUES ('sess1', 'entry', ?, ?)`, at, at); err != nil {
		t.Fatal(err)
	}
	if err := raw(t, s, `INSERT INTO turn_calls (id, session_id, turn_index, model, prompt_tokens, completion_tokens,
		cost_usd, latency_ms, reason, created_at) VALUES ('t1', 'sess1', 1, 'vendor/model', 10, 2, 0.0, 5, 'done', 'yesterday')`); err != nil {
		t.Fatal(err)
	}
	turns, err := s.TurnCalls(ctx, "sess1")
	if err != nil {
		t.Fatalf("TurnCalls: %v", err)
	}
	if len(turns) != 1 || !turns[0].CreatedAt.IsZero() {
		t.Fatalf("turns = %+v, want one row with the zero time", turns)
	}
}

// The session total is the sum of both ledgers with the covered part split out, and it says whether any of it is a
// price-list estimate. An allowance is not money spent, so the two numbers must not be conflated.
func TestSessionCostSplitsCoveredFromSpent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const at = "2026-11-02T10:00:00Z"
	if err := raw(t, s, `INSERT INTO sessions (id, kind, created_at, updated_at) VALUES ('sess1', 'entry', ?, ?)`, at, at); err != nil {
		t.Fatal(err)
	}
	// A pre-read the provider reported as real money, and a turn a plan's allowance covered.
	if err := s.RecordDecisionCall(ctx, domain.DecisionCall{
		SessionID: "sess1", Backend: "openrouter", Model: "jev-1.14",
		Odds: json.RawMessage(`{}`), CostUSD: 0.0002, CostSource: domain.CostSourceProvider,
	}); err != nil {
		t.Fatalf("RecordDecisionCall: %v", err)
	}
	if err := s.RecordTurn(ctx, domain.TurnCall{
		SessionID: "sess1", Model: "vendor/model", CostUSD: 0.001,
		CostSource: domain.CostSourceAllowance, AllowanceUSD: 0.001,
	}); err != nil {
		t.Fatalf("RecordTurn: %v", err)
	}

	cost, err := s.SessionCost(ctx, "sess1")
	if err != nil {
		t.Fatalf("SessionCost: %v", err)
	}
	if cost.Total < 0.00119 || cost.Total > 0.00121 {
		t.Errorf("total = %v, want 0.0012", cost.Total)
	}
	if cost.Covered < 0.00099 || cost.Covered > 0.00101 {
		t.Errorf("covered = %v, want 0.001", cost.Covered)
	}
	if cost.Estimated {
		t.Error("a provider-reported total was called an estimate")
	}
	if cost.Spent() < 0.000199 || cost.Spent() > 0.000201 {
		t.Errorf("spent = %v, want the uncovered 0.0002", cost.Spent())
	}

	// A catalog estimate is a guess, and the total says so rather than passing it off as a receipt.
	if catalogErr := s.RecordTurn(ctx, domain.TurnCall{
		SessionID: "sess1", Model: "vendor/model", CostUSD: 0.0005, CostSource: domain.CostSourceCatalog,
	}); catalogErr != nil {
		t.Fatalf("RecordTurn (catalog): %v", catalogErr)
	}
	cost, err = s.SessionCost(ctx, "sess1")
	if err != nil {
		t.Fatalf("SessionCost: %v", err)
	}
	if !cost.Estimated {
		t.Error("a catalog-priced call did not mark the total as an estimate")
	}
	if cost.Covered < 0.00099 || cost.Covered > 0.00101 {
		t.Errorf("covered = %v after a second call, want it unchanged at 0.001", cost.Covered)
	}
}

// A row written before the columns existed reads as real money with no allowance, which is what it was: the
// migration's defaults are the honest reading of an old receipt.
func TestAnOldLedgerRowReadsAsUncovered(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const at = "2026-11-02T10:00:00Z"
	if err := raw(t, s, `INSERT INTO sessions (id, kind, created_at, updated_at) VALUES ('sess1', 'entry', ?, ?)`, at, at); err != nil {
		t.Fatal(err)
	}
	// The insert names the columns 0005 had, so the two added later take their defaults.
	if err := raw(t, s, `INSERT INTO turn_calls (id, session_id, turn_index, model, prompt_tokens, completion_tokens,
		cost_usd, latency_ms, reason, created_at) VALUES ('t1', 'sess1', 1, 'vendor/model', 10, 2, 0.004, 5, 'done', ?)`, at); err != nil {
		t.Fatalf("insert: %v", err)
	}
	turns, err := s.TurnCalls(ctx, "sess1")
	if err != nil {
		t.Fatalf("TurnCalls: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	cost := turns[0].Cost()
	if cost.Total != 0.004 || cost.Covered != 0 || cost.Estimated {
		t.Fatalf("cost = %+v, want the old row as uncovered real money", cost)
	}
}
