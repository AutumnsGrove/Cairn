package world

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/AutumnsGrove/nova/decide"
	"github.com/AutumnsGrove/nova/provider"
)

// DecisionFault is a way the decisions backend misbehaves. It is sticky until cleared, like every fault in the
// world, so a retry cannot quietly hide a bug (docs/TESTING.md 1).
type DecisionFault string

const (
	// DecisionNone answers with the scripted odds.
	DecisionNone DecisionFault = ""
	// DecisionError is a provider outage: a retryable provider error, every time.
	DecisionError DecisionFault = "error"
	// DecisionSlow answers later than any sane deadline, honouring context cancellation.
	DecisionSlow DecisionFault = "slow"
	// DecisionMalformed answers with a check nobody asked about, which a caller must reject.
	DecisionMalformed DecisionFault = "malformed"
	// DecisionInconsistent answers a choice that contradicts its own probabilities.
	DecisionInconsistent DecisionFault = "inconsistent"
)

// DecisionUsage is what every fake call reports, so a test can assert the cost was recorded.
var DecisionUsage = provider.Usage{PromptTokens: 120, CompletionTokens: 0, TotalTokens: 120, CostUSD: 0.0002, CostSource: "provider"}

// DecisionCall is one recorded call. Only source names and their lengths are kept: the recorder must never
// become a copy of the operator's review text (docs/TESTING.md 3, docs/BUILD.md invariant 5).
type DecisionCall struct {
	Model    string
	Sources  []string
	SourceLn []int
	Question []string
	At       time.Time
}

// Decisions is the fake decision backend: scripted odds per check, faults, and a recorder.
type Decisions struct {
	clock Clock

	mu      sync.Mutex
	answers map[string]map[string]float64
	fault   DecisionFault
	calls   []DecisionCall
}

// NewDecisions builds the fake, with a fixed clock when the caller gives none.
func NewDecisions(clock Clock) *Decisions {
	if clock == nil {
		clock = RealClock{}
	}
	return &Decisions{clock: clock, answers: make(map[string]map[string]float64)}
}

// Set scripts the odds for one check. A check with no script answers its first option with certainty, which
// keeps a test that only cares about one check readable.
func (d *Decisions) Set(check string, probabilities map[string]float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	script := make(map[string]float64, len(probabilities))
	for option, p := range probabilities {
		script[option] = p
	}
	d.answers[check] = script
}

// SetFault makes every later call misbehave. The zero value clears it.
func (d *Decisions) SetFault(f DecisionFault) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fault = f
}

// Fault returns the current fault.
func (d *Decisions) Fault() DecisionFault {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fault
}

// Calls returns a copy of the recorded calls, oldest first.
func (d *Decisions) Calls() []DecisionCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DecisionCall, len(d.calls))
	copy(out, d.calls)
	return out
}

// Decide answers one decision call. It honours ctx, so a caller can prove its deadline.
func (d *Decisions) Decide(ctx context.Context, req decide.Request) (*decide.Response, error) {
	d.record(req)

	switch d.Fault() {
	case DecisionSlow:
		select {
		case <-time.After(slowDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case DecisionError:
		return nil, &provider.Error{Code: "unavailable", Message: "the decisions backend is down", Retryable: true}
	case DecisionMalformed:
		return &decide.Response{
			Answers: map[string]decide.Answer{
				"invented": {Choice: "whatever", Probabilities: map[string]float64{"whatever": 1}, Confidence: 1},
			},
			Usage: DecisionUsage,
			Model: "jev-1.14",
		}, nil
	case DecisionNone, DecisionInconsistent:
		// DecisionNone behaves; DecisionInconsistent answers and then contradicts itself below.
	}

	resp := &decide.Response{Answers: make(map[string]decide.Answer, len(req.Questions)), Usage: DecisionUsage, Model: "jev-1.14"}
	for name, question := range req.Questions {
		probabilities := d.script(name, question)
		choice := mostLikely(probabilities)
		if d.Fault() == DecisionInconsistent {
			// A real backend can report an option that its own distribution contradicts. Handing one back is
			// how a test proves the caller refuses to route on it.
			other := otherThan(probabilities, choice)
			probabilities[other] = 0.1
			probabilities[choice] = 0.9
			choice = other
		}
		resp.Answers[name] = decide.Answer{
			Kind:          decide.KindChoice,
			Choice:        choice,
			Probabilities: probabilities,
			Confidence:    0.9,
		}
	}
	return resp, nil
}

// script returns the probabilities for one check, restricted to the options the question actually offers so a
// caller's validation sees a coherent answer.
func (d *Decisions) script(check string, question decide.Question) map[string]float64 {
	d.mu.Lock()
	scripted := d.answers[check]
	d.mu.Unlock()

	options := make([]string, 0, len(question.Criteria))
	for option := range question.Criteria {
		options = append(options, option)
	}
	sort.Strings(options)

	out := make(map[string]float64, len(options))
	if scripted == nil {
		// Every option is present, so a test that contradicts the answer has something to contradict it with.
		for i, option := range options {
			if i == 0 {
				out[option] = 1
				continue
			}
			out[option] = 0
		}
		return out
	}
	// A partial script names only the interesting options; the rest of the mass is shared equally between the
	// options it did not name, so a test can say "medium is book 0.55, film 0.40" and mean it.
	var unnamed []string
	sum := 0.0
	for _, option := range options {
		p, ok := scripted[option]
		if !ok {
			unnamed = append(unnamed, option)
			continue
		}
		out[option] = p
		sum += p
	}
	if len(unnamed) == 0 {
		return out
	}
	rest := 1 - sum
	if rest < 0 {
		rest = 0
	}
	share := rest / float64(len(unnamed))
	for i, option := range unnamed {
		if i == len(unnamed)-1 {
			out[option] = rest - share*float64(len(unnamed)-1)
			continue
		}
		out[option] = share
	}
	return out
}

// mostLikely picks the highest probability, breaking ties by name so the fake is deterministic.
func mostLikely(probabilities map[string]float64) string {
	best, bestP := "", -1.0
	for option, p := range probabilities {
		if p > bestP || (p == bestP && option < best) {
			best, bestP = option, p
		}
	}
	return best
}

// otherThan picks a different option, which is how the fake contradicts itself.
func otherThan(probabilities map[string]float64, choice string) string {
	for option := range probabilities {
		if option != choice {
			return option
		}
	}
	return choice
}

// record keeps the shape of the call, never its text.
func (d *Decisions) record(req decide.Request) {
	call := DecisionCall{Model: req.Model, At: d.clock.Now()}
	switch state := req.State.(type) {
	case []decide.Source:
		for _, source := range state {
			call.Sources = append(call.Sources, source.Source)
			call.SourceLn = append(call.SourceLn, len(source.Text))
		}
	case string:
		call.Sources = []string{"state"}
		call.SourceLn = []int{len(state)}
	}
	for name := range req.Questions {
		call.Question = append(call.Question, name)
	}
	sort.Strings(call.Question)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call)
}
