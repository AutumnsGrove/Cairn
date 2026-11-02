package decisions

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// sumTolerance is how close a check's probabilities must come to 1. A decision model reports one distribution
// per check, so a set that sums to a half is not an opinion about the message: it is a broken answer, and the
// pre-read is dropped rather than routed on it (S05 "sad" route: a result that contradicts the parse).
const sumTolerance = 0.15

// Odds is one check's answer as it arrived: the chosen option, a probability per option and a confidence.
type Odds struct {
	Choice        string
	Probabilities map[string]float64
	Confidence    float64
}

// Session is what the session already knows. Inside an entry session the session decides what a message means;
// the pre-read only overrides that when it is very sure (PLAN.md 2, "decision routing").
type Session struct {
	// Active is true when a session is open, which is when the bias applies at all.
	Active bool
	// Intent is the session's own reading of a message: answer_pending while a card is open, log_review while a
	// draft is being built.
	Intent string
	// Medium is the medium of the work the session is about, when one has been chosen.
	Medium string
}

// Outcome is what policy makes of one pre-read: the routes that fired and the topics that were covered.
// Everything here is a hint for the agent; none of it is authority.
type Outcome struct {
	Intent    string
	Medium    string
	Format    string
	Revisit   bool
	Topics    []string
	Ambiguous []string
	Fired     []string
}

// Verdict is the fidelity check's answer. A bug is never shown as a warning: containment is proved in code, so
// adds_content means the code is wrong, not the model.
type Verdict struct {
	Verdict string
	Bug     bool
}

// Apply turns one pre-read's odds into routing. It is pure and deterministic, so the same odds always produce
// the same route, and every rule here is one from PLAN.md 2 that a test can point at.
func (r *Registry) Apply(answers map[string]Odds, session Session) Outcome {
	var out Outcome

	intent := r.Checks["intent"]
	intentFired := false
	if o, ok := answers["intent"]; ok {
		top := topNonQuiet(intent, o)
		if top != "" && probability(o, top) >= thresholdFor(intent, top) {
			out.Intent = top
			intentFired = true
		}
	}
	// The session's own reading stands unless the pre-read is sure enough to override it.
	if session.Active && session.Intent != "" && out.Intent != session.Intent {
		if probability(answers["intent"], out.Intent) < r.SessionOverride {
			out.Intent = session.Intent
			intentFired = true
		}
	}
	if intentFired {
		out.Fired = append(out.Fired, "intent")
	}

	for _, name := range r.Order {
		if name == "intent" {
			continue
		}
		c := r.Checks[name]
		if c.Phase != PhaseBefore {
			continue
		}
		if !allows(c, out.Intent) {
			continue
		}
		o, ok := answers[name]
		if !ok {
			continue
		}

		if c.Ambiguity != nil && name == "medium" {
			fired, ambiguous := r.splitMedium(c, o, session.Medium)
			if len(ambiguous) > 0 {
				out.Ambiguous = ambiguous
				out.Fired = append(out.Fired, name)
				continue
			}
			if fired == "" {
				continue
			}
			out.Medium = r.bias(session.Medium, fired, o)
			out.Fired = append(out.Fired, name)
			continue
		}

		top := topNonQuiet(c, o)
		if top == "" || probability(o, top) < thresholdFor(c, top) {
			continue
		}
		switch {
		case name == "format":
			out.Format = top
		case name == "revisit":
			out.Revisit = top == "yes"
			if !out.Revisit {
				continue
			}
		case strings.HasPrefix(name, "mentions_"):
			topic, ok := TopicForCheck(name)
			if !ok {
				continue
			}
			out.Topics = append(out.Topics, topic)
		default:
			// A check with no field of its own still fired; the caller reads its odds directly.
		}
		out.Fired = append(out.Fired, name)
	}

	slices.Sort(out.Topics)
	return out
}

// splitMedium applies the split-read rule and returns the chosen medium, or the two candidates to ask about.
// A session that already knows its work settles the question: asking her what she is reviewing when the session
// is about her book is a worse guess than the session's own state.
func (r *Registry) splitMedium(c *Check, o Odds, sessionMedium string) (string, []string) {
	ranked := rankNonQuiet(c, o)
	if len(ranked) == 0 {
		return "", nil
	}
	top := ranked[0]
	// The split rule runs before the threshold: two real candidates below the bar is exactly the case the
	// question exists for, and asking is cheaper than guessing wrong (mockups 1, "Book or film?").
	if c.Ambiguity != nil && len(ranked) > 1 {
		second := ranked[1]
		if probability(o, second) >= c.Ambiguity.RunnerUpMin &&
			probability(o, top)-probability(o, second) < c.Ambiguity.MaxGap {
			if sessionMedium == top || sessionMedium == second {
				return sessionMedium, nil
			}
			return "", []string{top, second}
		}
	}
	if probability(o, top) < thresholdFor(c, top) {
		return "", nil
	}
	return top, nil
}

// bias lets the session's medium stand unless the pre-read is sure. A session with no medium has no opinion.
func (r *Registry) bias(sessionMedium, read string, o Odds) string {
	if sessionMedium == "" || sessionMedium == read {
		return read
	}
	if probability(o, read) < r.SessionOverride {
		return sessionMedium
	}
	return read
}

// allows applies only_when_intent and suppress_when_intent to the effective intent. An empty intent matches no
// list, so a check that depends on log_review never acts when nothing was routed.
func allows(c *Check, intent string) bool {
	if len(c.OnlyWhenIntent) > 0 && !slices.Contains(c.OnlyWhenIntent, intent) {
		return false
	}
	return !slices.Contains(c.SuppressWhenIntent, intent)
}

// thresholdFor is the bar this option must clear: its own when it has one, the check's otherwise.
func thresholdFor(c *Check, option string) float64 {
	if t, ok := c.OptionThresholds[option]; ok {
		return t
	}
	return c.Threshold
}

// probability reads an option's probability, defaulting an absent one to zero.
func probability(o Odds, option string) float64 {
	return o.Probabilities[option]
}

// rankNonQuiet returns the check's options by probability, highest first, excluding the quiet one. Ties break by
// name, so two runs on one answer produce one order.
func rankNonQuiet(c *Check, o Odds) []string {
	names := c.OptionNames()
	var ranked []string
	for _, name := range names {
		if name != c.Quiet {
			ranked = append(ranked, name)
		}
	}
	slices.SortStableFunc(ranked, func(a, b string) int {
		switch pa, pb := probability(o, a), probability(o, b); {
		case pa > pb:
			return -1
		case pa < pb:
			return 1
		default:
			return strings.Compare(a, b)
		}
	})
	return ranked
}

// topNonQuiet is the single most likely option that is not the quiet default.
func topNonQuiet(c *Check, o Odds) string {
	ranked := rankNonQuiet(c, o)
	if len(ranked) == 0 {
		return ""
	}
	return ranked[0]
}

// CheckOdds rejects an answer that cannot be trusted before any rule looks at it: a missing check, an option
// that was never asked about, a negative or non-normalised distribution, or a choice that contradicts its own
// probabilities. The caller treats a failure as "no pre-read" and says so (S05 "sad" route).
func (r *Registry) CheckOdds(answers map[string]Odds) error {
	return r.validateAnswers(r.Before(), answers)
}

// CheckPostOdds is the same check for the second call's checks: the fidelity question is validated against its
// own single-answer set, never against the first call's.
func (r *Registry) CheckPostOdds(answers map[string]Odds) error {
	return r.validateAnswers(r.Post(), answers)
}

// validateAnswers checks the answers for exactly this set of checks and nothing else.
func (r *Registry) validateAnswers(checks []*Check, answers map[string]Odds) error {
	asked := make(map[string]bool)
	for _, c := range checks {
		asked[c.Name] = true
		o, ok := answers[c.Name]
		if !ok {
			return fmt.Errorf("decisions: no answer for check %q", c.Name)
		}
		if err := checkOne(c, o); err != nil {
			return err
		}
	}
	for name := range answers {
		if !asked[name] {
			return fmt.Errorf("decisions: answer for check %q, which was not asked", name)
		}
	}
	return nil
}

// checkOne validates one check's distribution.
func checkOne(c *Check, o Odds) error {
	sum := 0.0
	best, bestP := "", -1.0
	for option, p := range o.Probabilities {
		if _, ok := c.Options[option]; !ok {
			return fmt.Errorf("decisions: check %q has a probability for %q, which is not one of its options", c.Name, option)
		}
		if p < 0 || p > 1 || math.IsNaN(p) {
			return fmt.Errorf("decisions: check %q has a negative or impossible probability %v for %q", c.Name, p, option)
		}
		sum += p
		// A tie is not a contradiction, and an alphabetical tiebreak keeps this deterministic.
		if p > bestP || (p == bestP && option < best) {
			best, bestP = option, p
		}
	}
	if math.Abs(sum-1) > sumTolerance {
		return fmt.Errorf("decisions: check %q does not sum to one (%v)", c.Name, sum)
	}
	if o.Choice != "" && o.Choice != best {
		return fmt.Errorf("decisions: check %q says %q but its probabilities favour %q; the answer contradicts itself", c.Name, o.Choice, best)
	}
	if o.Confidence < 0 || o.Confidence > 1 {
		return fmt.Errorf("decisions: check %q has confidence %v outside 0 to 1", c.Name, o.Confidence)
	}
	return nil
}

// unavailable marks a pre-read that could not run, with a reason the UI can put in plain words.
func unavailable(reason string, err error) error {
	return &ErrUnavailable{Reason: reason, Err: err}
}

// FidelityVerdict reads the fidelity check's odds. A logged bug wins over a warning, and a faithful extract
// produces neither.
func (c *Check) FidelityVerdict(o Odds) Verdict {
	for _, option := range c.LogAsBug {
		if probability(o, option) >= thresholdFor(c, option) {
			return Verdict{Bug: true}
		}
	}
	best := ""
	for _, option := range rankNonQuiet(c, o) {
		if option == c.Quiet {
			continue
		}
		if _, own := c.OptionThresholds[option]; !own {
			continue
		}
		if probability(o, option) < thresholdFor(c, option) {
			continue
		}
		if best == "" || probability(o, option) > probability(o, best) {
			best = option
		}
	}
	return Verdict{Verdict: best}
}

// ErrUnavailable marks a pre-read that could not run, with a reason the UI can put in plain words. It is never a
// reason to block a turn: the caller proceeds and says the quick read was not available (PLAN.md 2).
type ErrUnavailable struct {
	Reason string
	Err    error
}

// Error renders the reason with the underlying cause, which is for a log and not for her.
func (e *ErrUnavailable) Error() string {
	if e.Err == nil {
		return "decisions: pre-read unavailable: " + e.Reason
	}
	return "decisions: pre-read unavailable: " + e.Reason + ": " + e.Err.Error()
}

// Unwrap keeps the cause for errors.Is and errors.As.
func (e *ErrUnavailable) Unwrap() error { return e.Err }

// ErrNotConfigured is the typed form of "the decisions layer is off", which is a normal runtime state
// (docs/BUILD.md invariant 13), not a failure.
var ErrNotConfigured = errors.New("decisions: no backend configured")
