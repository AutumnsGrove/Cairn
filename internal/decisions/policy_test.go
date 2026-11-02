package decisions

import (
	"strings"
	"testing"
)

// A registry with the shipped checks is the fixture: routing rules are only meaningful against the real names.
func policyFixture(t *testing.T) *Registry {
	t.Helper()
	r, err := Load("../../config/decisions")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return r
}

// odds builds one check's answer from option/probability pairs.
func odds(choice string, pairs ...any) Odds {
	o := Odds{Choice: choice, Probabilities: map[string]float64{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		o.Probabilities[pairs[i].(string)] = toFloat(pairs[i+1])
	}
	return o
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	panic("odds: unsupported probability")
}

func TestApplyRoutesALogReview(t *testing.T) {
	r := policyFixture(t)
	out := r.Apply(map[string]Odds{
		"intent":          odds("log_review", "log_review", .93, "chitchat", .05),
		"medium":          odds("book", "book", .81, "film", .14, "unknown", .05),
		"format":          odds("print", "print", .90, "not_stated", .04),
		"revisit":         odds("no", "no", .90, "yes", .10),
		"mentions_story":  odds("yes", "yes", .91, "no", .09),
		"mentions_sound":  odds("no", "no", .80, "yes", .20),
		"mentions_acting": odds("no", "no", .95, "yes", .05),
	}, Session{})

	if out.Intent != "log_review" {
		t.Errorf("intent = %q", out.Intent)
	}
	if out.Medium != "book" {
		t.Errorf("medium = %q", out.Medium)
	}
	if out.Format != "print" {
		t.Errorf("format = %q", out.Format)
	}
	if out.Revisit {
		t.Error("revisit fired at .10")
	}
	if len(out.Topics) != 1 || out.Topics[0] != "story" {
		t.Errorf("topics = %v, want just story", out.Topics)
	}
	for _, want := range []string{"intent", "medium", "format", "mentions_story"} {
		if !containsString(out.Fired, want) {
			t.Errorf("fired = %v, want %q", out.Fired, want)
		}
	}
	for _, unwanted := range []string{"revisit", "mentions_sound"} {
		if containsString(out.Fired, unwanted) {
			t.Errorf("fired = %v, want no %q", out.Fired, unwanted)
		}
	}
}

// The quiet option is the whole safety property: a wrong guess costs nothing because nothing acts.
func TestApplyQuietDefault(t *testing.T) {
	r := policyFixture(t)
	out := r.Apply(map[string]Odds{
		"intent":         odds("chitchat", "chitchat", .90, "log_review", .05),
		"medium":         odds("book", "book", .99, "unknown", .01),
		"format":         odds("print", "print", .99, "not_stated", .01),
		"revisit":        odds("yes", "yes", .99, "no", .01),
		"mentions_story": odds("yes", "yes", .99, "no", .01),
	}, Session{})

	if out.Intent != "" {
		t.Errorf("intent = %q, want nothing fired", out.Intent)
	}
	if out.Medium != "" || out.Format != "" || out.Revisit || len(out.Topics) != 0 {
		t.Errorf("a quiet intent still acted: %+v", out)
	}
	if len(out.Fired) != 0 {
		t.Errorf("fired = %v, want none", out.Fired)
	}
}

func TestApplyThreshold(t *testing.T) {
	r := policyFixture(t)
	cases := map[string]struct {
		p    float64
		want string
	}{
		"below":      {.50, ""},
		"at":         {.65, "log_review"},
		"above":      {.70, "log_review"},
		"quiet wins": {.40, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := r.Apply(map[string]Odds{
				"intent": odds("log_review", "log_review", tc.p, "chitchat", 1-tc.p),
			}, Session{})
			if out.Intent != tc.want {
				t.Errorf("intent = %q at %v, want %q", out.Intent, tc.p, tc.want)
			}
		})
	}
}

// An option may need a higher bar than the rest of its check, so edit_entry can be rare without making intent
// deaf.
func TestApplyOptionThreshold(t *testing.T) {
	r := policyFixture(t)
	intent := map[string]Odds{
		"intent": odds("log_review", "log_review", .90, "edit_entry", .10),
	}
	if out := r.Apply(intent, Session{}); out.Intent != "log_review" {
		t.Errorf("intent = %q", out.Intent)
	}
	below := r.Apply(map[string]Odds{
		"intent": odds("edit_entry", "edit_entry", .75, "log_review", .20),
	}, Session{})
	if below.Intent != "" {
		t.Errorf("edit_entry fired at .75, below its 0.80 bar: %q", below.Intent)
	}
	above := r.Apply(map[string]Odds{
		"intent": odds("edit_entry", "edit_entry", .85, "log_review", .10),
	}, Session{})
	if above.Intent != "edit_entry" {
		t.Errorf("edit_entry at .85 = %q", above.Intent)
	}
}

// Dune is the split read from the mockups: book .55 and film .40 asks "which one?" instead of guessing.
func TestApplyMediumAmbiguity(t *testing.T) {
	r := policyFixture(t)
	intent := odds("log_review", "log_review", .95, "chitchat", .02)

	split := r.Apply(map[string]Odds{
		"intent": intent,
		"medium": odds("book", "book", .55, "film", .40, "unknown", .05),
	}, Session{})
	if split.Medium != "" {
		t.Errorf("medium = %q, want the question instead of a guess", split.Medium)
	}
	if len(split.Ambiguous) != 2 || split.Ambiguous[0] != "book" || split.Ambiguous[1] != "film" {
		t.Errorf("ambiguous = %v, want [book film]", split.Ambiguous)
	}
	if !containsString(split.Fired, "medium") {
		t.Errorf("fired = %v, want medium", split.Fired)
	}

	clear := r.Apply(map[string]Odds{
		"intent": intent,
		"medium": odds("book", "book", .90, "film", .05, "unknown", .05),
	}, Session{})
	if clear.Medium != "book" || len(clear.Ambiguous) != 0 {
		t.Errorf("clear medium = %q, ambiguous = %v", clear.Medium, clear.Ambiguous)
	}

	// A session that already knows the work settles the split without a card.
	settled := r.Apply(map[string]Odds{
		"intent": intent,
		"medium": odds("book", "book", .55, "film", .40, "unknown", .05),
	}, Session{Active: true, Medium: "book"})
	if settled.Medium != "book" || len(settled.Ambiguous) != 0 {
		t.Errorf("session medium did not settle the split: medium = %q, ambiguous = %v", settled.Medium, settled.Ambiguous)
	}

	// Runner-up high but the leader clearly ahead: no question.
	ahead := r.Apply(map[string]Odds{
		"intent": intent,
		"medium": odds("book", "book", .80, "film", .30, "unknown", .05),
	}, Session{})
	if ahead.Medium != "book" {
		t.Errorf("leader .50 ahead = %q, ambiguous %v", ahead.Medium, ahead.Ambiguous)
	}
}

// Inside an entry session the session decides, unless the pre-read is very sure.
func TestApplySessionBias(t *testing.T) {
	r := policyFixture(t)
	session := Session{Active: true, Intent: "answer_pending"}

	weak := r.Apply(map[string]Odds{
		"intent": odds("log_review", "log_review", .70, "answer_pending", .20),
	}, session)
	if weak.Intent != "answer_pending" {
		t.Errorf("intent = %q at .70 in a session, want the session's answer_pending", weak.Intent)
	}

	strong := r.Apply(map[string]Odds{
		"intent": odds("log_review", "log_review", .85, "answer_pending", .10),
	}, session)
	if strong.Intent != "log_review" {
		t.Errorf("intent = %q at .85, want log_review to override the session", strong.Intent)
	}

	// The quiet option at .99 still does not beat an open question: a reply to a card is an answer.
	quiet := r.Apply(map[string]Odds{
		"intent": odds("chitchat", "chitchat", .99, "answer_pending", .01),
	}, session)
	if quiet.Intent != "answer_pending" {
		t.Errorf("intent = %q, want the session's answer_pending", quiet.Intent)
	}

	mediumSession := Session{Active: true, Medium: "tv"}
	wrong := r.Apply(map[string]Odds{
		"intent": odds("log_review", "log_review", .95, "chitchat", .02),
		"medium": odds("film", "film", .70, "tv", .25, "unknown", .05),
	}, mediumSession)
	if wrong.Medium != "tv" {
		t.Errorf("medium = %q, want the session's tv at .70", wrong.Medium)
	}
	sure := r.Apply(map[string]Odds{
		"intent": odds("log_review", "log_review", .95, "chitchat", .02),
		"medium": odds("film", "film", .90, "tv", .05, "unknown", .05),
	}, mediumSession)
	if sure.Medium != "film" {
		t.Errorf("medium = %q at .90, want film to override the session", sure.Medium)
	}
}

// only_when_intent and suppression are applied after the one batched call, against the effective intent.
func TestApplyIntentGates(t *testing.T) {
	r := policyFixture(t)
	assertNotFired := func(t *testing.T, out Outcome, check string) {
		t.Helper()
		if containsString(out.Fired, check) {
			t.Errorf("fired = %v, want no %q", out.Fired, check)
		}
	}

	// format is only_when_intent: log_review, so a find_entries read leaves it alone.
	chitchat := r.Apply(map[string]Odds{
		"intent": odds("find_entries", "find_entries", .90, "chitchat", .05),
		"format": odds("print", "print", .99, "not_stated", .01),
	}, Session{})
	if chitchat.Format != "" {
		t.Errorf("format = %q, want it gated out", chitchat.Format)
	}
	assertNotFired(t, chitchat, "format")

	r2, err := LoadFiles([]byte(strings.Replace(baseWording, `  mentions_sound:`, `  format:
    quiet: not_stated
    instructions: How?
    options:
      not_stated: Not said.
      print: Read it as a book.
  mentions_sound:`, 1)), []byte(strings.Replace(basePolicy, `  mentions_defaults:`, `    format:
      threshold: 0.75
      suppress_when_intent: [log_review]
  mentions_defaults:`, 1)))
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	out := r2.Apply(map[string]Odds{
		"intent": odds("log_review", "log_review", .95, "chitchat", .02),
		"format": odds("print", "print", .99, "not_stated", .01),
	}, Session{})
	if out.Format != "" {
		t.Errorf("format = %q, want it suppressed by intent=log_review", out.Format)
	}
}

func TestCheckOddsRejectsIncoherence(t *testing.T) {
	r := policyFixture(t)
	good := map[string]Odds{
		"intent":              odds("log_review", "log_review", .9, "chitchat", .1),
		"medium":              odds("book", "book", .9, "film", .1),
		"format":              odds("print", "print", .9, "not_stated", .1),
		"revisit":             odds("no", "no", .9, "yes", .1),
		"mentions_sound":      odds("no", "no", .9, "yes", .1),
		"mentions_visuals":    odds("no", "no", .9, "yes", .1),
		"mentions_acting":     odds("no", "no", .9, "yes", .1),
		"mentions_story":      odds("no", "no", .9, "yes", .1),
		"mentions_pacing":     odds("no", "no", .9, "yes", .1),
		"mentions_ending":     odds("no", "no", .9, "yes", .1),
		"mentions_characters": odds("no", "no", .9, "yes", .1),
		"mentions_world":      odds("no", "no", .9, "yes", .1),
		"mentions_writing":    odds("no", "no", .9, "yes", .1),
	}
	if err := r.CheckOdds(good); err != nil {
		t.Fatalf("CheckOdds(good): %v", err)
	}

	cases := map[string]struct {
		mutate func(map[string]Odds)
		want   string
	}{
		"missing answer": {
			func(m map[string]Odds) { delete(m, "mentions_world") },
			"no answer for",
		},
		"probabilities do not sum": {
			func(m map[string]Odds) { m["medium"] = odds("book", "book", .5, "film", .1) },
			"does not sum",
		},
		"negative probability": {
			func(m map[string]Odds) { m["medium"] = odds("book", "book", 1.2, "film", -.2) },
			"negative",
		},
		"unknown option": {
			func(m map[string]Odds) { m["medium"] = odds("book", "book", .9, "film", .1, "poem", 0) },
			"not one of its options",
		},
		"choice is not the most likely": {
			func(m map[string]Odds) { m["medium"] = odds("film", "book", .8, "film", .2) },
			"contradicts",
		},
		"post-extract check sent early": {
			func(m map[string]Odds) { m["extract_fidelity"] = odds("faithful", "faithful", 1) },
			"not asked",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := make(map[string]Odds, len(good))
			for k, v := range good {
				m[k] = v
			}
			tc.mutate(m)
			err := r.CheckOdds(m)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckOdds = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestFidelityVerdict(t *testing.T) {
	r := policyFixture(t)
	check := r.Checks["extract_fidelity"]

	faithful := check.FidelityVerdict(odds("faithful", "faithful", .9, "drops_opinion", .1, "adds_content", .01))
	if faithful.Verdict != "" {
		t.Errorf("verdict = %q, want none", faithful.Verdict)
	}
	drops := check.FidelityVerdict(odds("drops_opinion", "faithful", .2, "drops_opinion", .75, "adds_content", .05))
	if drops.Verdict != "drops_opinion" || drops.Bug {
		t.Errorf("verdict = %+v", drops)
	}
	// adds_content should be impossible once containment is checked in code: it is a bug, not a warning.
	adds := check.FidelityVerdict(odds("adds_content", "faithful", .1, "drops_opinion", .1, "adds_content", .8))
	if !adds.Bug {
		t.Errorf("verdict = %+v, want Bug", adds)
	}
	if adds.Verdict != "" {
		t.Errorf("verdict = %q, want no normal warning for a bug", adds.Verdict)
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
