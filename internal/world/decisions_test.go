package world

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AutumnsGrove/nova/decide"
	"github.com/AutumnsGrove/nova/provider"
)

// decisionRequest is a two-check call shaped like a real pre-read: separate sources, named questions.
func decisionRequest() decide.Request {
	return decide.Request{
		Model: "cairn_preread",
		State: []decide.Source{
			{Source: "message", Text: "I finished Dune last night and loved the ending."},
			{Source: "work", Text: "Dune"},
		},
		Questions: map[string]decide.Question{
			"intent": {
				Instructions: "What is this?",
				Criteria:     map[string]string{"chitchat": "Nothing much.", "log_review": "A review."},
			},
			"medium": {
				Instructions: "What kind of thing?",
				Criteria:     map[string]string{"book": "A book.", "film": "A film.", "unknown": "Not clear."},
			},
		},
	}
}

func TestDecisionsAnswersAndRecords(t *testing.T) {
	clock := NewFixedClock(time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC))
	d := NewDecisions(clock)
	d.Set("intent", map[string]float64{"log_review": 0.93})
	d.Set("medium", map[string]float64{"book": 0.81, "film": 0.14})

	resp, err := d.Decide(context.Background(), decisionRequest())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := resp.Answers["intent"].Choice; got != "log_review" {
		t.Errorf("intent choice = %q", got)
	}
	if got := resp.Answers["medium"].Probabilities["book"]; got != 0.81 {
		t.Errorf("book odds = %v", got)
	}
	// A partial script carries the rest of the mass on the first option in alphabetical order.
	if got := resp.Answers["medium"].Probabilities["unknown"]; got < 0.04 || got > 0.06 {
		t.Errorf("unknown odds = %v, want the remainder", got)
	}
	if resp.Usage.CostUSD != 0.0002 || resp.Model != "jev-1.14" {
		t.Errorf("response = %+v", resp)
	}

	calls := d.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls recorded", len(calls))
	}
	call := calls[0]
	if call.Model != "cairn_preread" {
		t.Errorf("model = %q", call.Model)
	}
	if len(call.Sources) != 2 || call.Sources[0] != "message" || call.Sources[1] != "work" {
		t.Errorf("sources = %v", call.Sources)
	}
	if call.SourceLn[0] != len("I finished Dune last night and loved the ending.") {
		t.Errorf("source lengths = %v", call.SourceLn)
	}
	if len(call.Question) != 2 || call.Question[0] != "intent" || call.Question[1] != "medium" {
		t.Errorf("questions = %v", call.Question)
	}
	if !call.At.Equal(clock.Now()) {
		t.Errorf("recorded at %v, want %v", call.At, clock.Now())
	}
}

// A check with no script answers its first option, so a test that only cares about one check stays short.
func TestDecisionsUnscriptedCheck(t *testing.T) {
	d := NewDecisions(nil)
	resp, err := d.Decide(context.Background(), decisionRequest())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got := resp.Answers["medium"].Choice; got != "book" {
		t.Errorf("medium choice = %q, want the first option alphabetically", got)
	}
}

func TestDecisionsFaults(t *testing.T) {
	ctx := context.Background()

	t.Run("error is sticky and retryable", func(t *testing.T) {
		d := NewDecisions(nil)
		d.SetFault(DecisionError)
		for i := 0; i < 3; i++ {
			_, err := d.Decide(ctx, decisionRequest())
			var pe *provider.Error
			if !errors.As(err, &pe) || !pe.Retryable {
				t.Fatalf("call %d error = %v, want a retryable provider error", i+1, err)
			}
		}
	})

	t.Run("slow honours the deadline", func(t *testing.T) {
		d := NewDecisions(nil)
		d.SetFault(DecisionSlow)
		child, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := d.Decide(child, decisionRequest())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want a deadline", err)
		}
		if elapsed := time.Since(start); elapsed > slowDelay {
			t.Errorf("slow fault outlived its deadline: %v", elapsed)
		}
	})

	t.Run("malformed answers a check nobody asked", func(t *testing.T) {
		d := NewDecisions(nil)
		d.SetFault(DecisionMalformed)
		resp, err := d.Decide(ctx, decisionRequest())
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if err := decide.ValidateResponse(decisionRequest(), resp); err == nil {
			t.Fatal("ValidateResponse accepted an answer for a check nobody asked")
		}
	})

	t.Run("inconsistent contradicts its own odds", func(t *testing.T) {
		d := NewDecisions(nil)
		d.SetFault(DecisionInconsistent)
		resp, err := d.Decide(ctx, decisionRequest())
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		answer := resp.Answers["medium"]
		if answer.Probabilities[answer.Choice] == maxOf(answer.Probabilities) {
			t.Fatalf("answer %+v is not contradictory", answer)
		}
	})

	t.Run("a cleared fault behaves again", func(t *testing.T) {
		d := NewDecisions(nil)
		d.SetFault(DecisionError)
		if _, err := d.Decide(ctx, decisionRequest()); err == nil {
			t.Fatal("no error while the fault was set")
		}
		d.SetFault(DecisionNone)
		if _, err := d.Decide(ctx, decisionRequest()); err != nil {
			t.Fatalf("Decide after clearing the fault: %v", err)
		}
	})
}

// A cancelled caller is a sad route of its own: the fake must return the cancellation, not a timeout of its own.
func TestDecisionsCancelled(t *testing.T) {
	d := NewDecisions(nil)
	d.SetFault(DecisionSlow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Decide(ctx, decisionRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func maxOf(probabilities map[string]float64) float64 {
	best := -1.0
	for _, p := range probabilities {
		if p > best {
			best = p
		}
	}
	return best
}
