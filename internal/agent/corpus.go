package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// The fixture corpus and its runner. Every case is invented: no real review, title, person or screenshot appears in
// the repository (docs/BUILD.md invariant 10). A case records what she typed, the pre-read odds, the tool sequence
// the model should take, and the fields the card must end up with.
//
// The runner in CI drives the fake model, so it proves the pipeline's plumbing and every guardrail. The operator's
// live run uses the same cases against the real model, which is where the odds table and the thresholds come from.

// MaxCorpusBytes bounds the corpus file. It is repo data, not user input, but an unbounded read is still a bug.
const MaxCorpusBytes = 1 << 20

// Case is one invented message and the outcome the pipeline must produce.
type Case struct {
	ID      string `json:"id"`
	Group   string `json:"group"` // book, film_tv or cross
	Message string `json:"message"`
	Medium  string `json:"medium"`
	// Provider and ProviderID name the work the scripted model picks. Empty means the case builds no card.
	Provider   string `json:"provider,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	Title      string `json:"title,omitempty"`

	// Preread is the odds the fake decisions backend returns for this case.
	Preread map[string]map[string]float64 `json:"preread"`

	// Tools is the model's tool sequence, in order.
	Tools []string `json:"tools,omitempty"`
	// AskHints are the chips the scripted model points at.
	AskHints map[string][]string `json:"ask_hints,omitempty"`
	// BodyQuote and VerdictQuote are what the scripted model quotes.
	BodyQuote    string `json:"body_quote,omitempty"`
	VerdictQuote string `json:"verdict_quote,omitempty"`

	// The awkward paths.
	FillRating     string   `json:"fill_rating,omitempty"`
	FillBody       string   `json:"fill_body,omitempty"`
	HugeTitle      bool     `json:"huge_title,omitempty"`
	LookupFault    bool     `json:"lookup_fault,omitempty"`
	LookupChoice   string   `json:"lookup_choice,omitempty"`
	Revisit        bool     `json:"revisit,omitempty"`
	RevisitChoice  string   `json:"revisit_choice,omitempty"`
	Ambiguous      []string `json:"ambiguous,omitempty"`
	ChooseMedium   string   `json:"choose_medium,omitempty"`
	HostileQuote   string   `json:"hostile_quote,omitempty"`
	TitleInjection bool     `json:"title_injection,omitempty"`
	Malformed      bool     `json:"malformed,omitempty"`
	StepCap        bool     `json:"step_cap,omitempty"`

	// Expected is what the runner asserts.
	Expected Expectation `json:"expected"`

	Hostile bool   `json:"hostile,omitempty"`
	Awkward string `json:"awkward,omitempty"`
}

// Expectation is the card and routing a case must produce.
type Expectation struct {
	Intent     string   `json:"intent"`
	Medium     string   `json:"medium"`
	Ambiguous  []string `json:"ambiguous,omitempty"`
	Rating     int      `json:"rating"`
	Date       string   `json:"date"`
	Format     string   `json:"format"`
	Body       string   `json:"body,omitempty"`
	Verdict    string   `json:"verdict,omitempty"`
	WorkTitle  string   `json:"work_title,omitempty"`
	ManualWork bool     `json:"manual_work,omitempty"`
	NoCard     bool     `json:"no_card,omitempty"`
	Notice     string   `json:"notice,omitempty"`
	Revises    bool     `json:"revises,omitempty"`
}

// CaseResult is one case's outcome.
type CaseResult struct {
	ID       string
	Group    string
	Passed   bool
	Failures []string
	// Odds is the pre-read's full probability vector, kept for the odds table.
	Odds map[string]map[string]float64
}

// Report is the whole corpus run.
type Report struct {
	Cases  int
	Passed int
	Failed []CaseResult
	// Odds is every case's pre-read vector, keyed by case then check then option. It is the table a threshold is
	// proposed from; the real run's table is the operator's.
	Odds map[string]map[string]map[string]float64
}

// Summary is the one line a CI log and a handoff both use.
func (r Report) Summary() string {
	if len(r.Failed) == 0 {
		return fmt.Sprintf("corpus: %d/%d cases pass", r.Passed, r.Cases)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "corpus: %d/%d cases pass; failures:", r.Passed, r.Cases)
	for _, failure := range r.Failed {
		fmt.Fprintf(&b, "\n  %s: %s", failure.ID, strings.Join(failure.Failures, "; "))
	}
	return b.String()
}

// LoadCorpus reads and validates the corpus file. A malformed case is a load error with its id, so a broken fixture
// is noticed before a run rather than as a mystery failure.
func LoadCorpus(path string) ([]Case, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("corpus: %w", err)
	}
	if info.Size() > MaxCorpusBytes {
		return nil, fmt.Errorf("corpus: %s is over the %d-byte cap", path, MaxCorpusBytes)
	}
	body, err := os.ReadFile(path) //nolint:gosec // the path is a committed fixture, not user input
	if err != nil {
		return nil, fmt.Errorf("corpus: %w", err)
	}
	return ParseCorpus(body)
}

// ParseCorpus decodes and validates a corpus. It is separate from LoadCorpus so a test can drive it with bytes.
func ParseCorpus(body []byte) ([]Case, error) {
	var cases []Case
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cases); err != nil {
		return nil, fmt.Errorf("corpus: %w", err)
	}
	if len(cases) == 0 {
		return nil, errors.New("corpus: no cases")
	}
	seen := make(map[string]bool, len(cases))
	for i, c := range cases {
		if strings.TrimSpace(c.ID) == "" {
			return nil, fmt.Errorf("corpus: case %d has no id", i)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("corpus: duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if strings.TrimSpace(c.Message) == "" {
			return nil, fmt.Errorf("corpus: %s has no message", c.ID)
		}
		switch c.Group {
		case "book", "film_tv", "cross":
		default:
			return nil, fmt.Errorf("corpus: %s has unknown group %q", c.ID, c.Group)
		}
	}
	return cases, nil
}

// RunCorpus runs every case and collects the failures instead of stopping at the first one. A panic in one case is
// converted into that case's failure, so a runner bug cannot hide the rest of the corpus.
func RunCorpus(ctx context.Context, cases []Case, runOne func(context.Context, Case) CaseResult) Report {
	report := Report{Cases: len(cases), Odds: make(map[string]map[string]map[string]float64, len(cases))}
	for _, c := range cases {
		result := runSafely(ctx, c, runOne)
		if result.Odds != nil {
			report.Odds[c.ID] = result.Odds
		}
		if result.Passed {
			report.Passed++
			continue
		}
		report.Failed = append(report.Failed, result)
	}
	sort.Slice(report.Failed, func(i, j int) bool { return report.Failed[i].ID < report.Failed[j].ID })
	return report
}

func runSafely(ctx context.Context, c Case, runOne func(context.Context, Case) CaseResult) (result CaseResult) {
	defer func() {
		if rec := recover(); rec != nil {
			result = CaseResult{ID: c.ID, Group: c.Group, Failures: []string{fmt.Sprintf("panic: %v", rec)}}
		}
	}()
	result = runOne(ctx, c)
	result.ID = c.ID
	result.Group = c.Group
	if len(result.Failures) == 0 {
		result.Passed = true
	}
	return result
}
