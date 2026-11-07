package domain

import (
	"math"
	"strings"
	"testing"
	"time"
)

// The edge cases behind the branchy helpers. They are here so the package's 95% coverage floor means the edges
// were considered, not that the floor was lowered.

func TestResolveDayMonthWithoutAYear(t *testing.T) {
	today := time.Date(2026, time.October, 30, 0, 0, 0, 0, london)

	// A date earlier this year stays this year.
	if got := ResolveDate("read it on 3 March", today, london); got.Date.Format("2006-01-02") != "2026-03-03" {
		t.Errorf("date = %s, want 2026-03-03", got.Date.Format("2006-01-02"))
	}
	// A date later this year means last year, because you cannot finish something in the future.
	if got := ResolveDate("read it on 12 December", today, london); got.Date.Format("2006-01-02") != "2025-12-12" {
		t.Errorf("date = %s, want 2025-12-12", got.Date.Format("2006-01-02"))
	}
}

func TestMakeDateRejectsImpossibleValues(t *testing.T) {
	cases := [][3]string{
		{"2026", "13", "1"},
		{"2026", "1", "32"},
		{"abc", "1", "1"},
		{"26", "1", "1"},
		{"2026", "2", "30"},
	}
	for _, c := range cases {
		if _, ok := makeDate(c[0], c[1], c[2], london); ok {
			t.Errorf("makeDate(%v) accepted an impossible date", c)
		}
	}
}

func TestExpandYear(t *testing.T) {
	if got := expandYear("25"); got != 2025 {
		t.Errorf("expandYear(25) = %d", got)
	}
	if got := expandYear("2025"); got != 2025 {
		t.Errorf("expandYear(2025) = %d", got)
	}
	if got := expandYear("nope"); got != 0 {
		t.Errorf("expandYear(nope) = %d", got)
	}
}

func TestGuardErrorString(t *testing.T) {
	err := &GuardError{Code: CodeRatingRequired, Message: "Add a rating."}
	if !strings.Contains(err.Error(), CodeRatingRequired) {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestRatingEdgeCases(t *testing.T) {
	// A denominator we do not use is not a rating scale.
	if got := ParseRating("7/12"); got.Status != RatingNone {
		t.Errorf("7/12 = %+v, want none", got)
	}
	// More than five glyphs is still five stars.
	if got := ParseRating("★★★★★★"); got.Quarters != 20 || got.Status != RatingStated {
		t.Errorf("six glyphs = %+v, want 5 stars", got)
	}
	// A NaN or an out-of-range float from a model is no rating, never a clamp.
	for _, v := range []float64{math.NaN(), math.Inf(1), -1, 6, 0} {
		if got := RatingFromStars(v); got != 0 {
			t.Errorf("RatingFromStars(%v) = %d, want 0", v, got)
		}
	}
	// A fraction whose numerator is not a number is ignored rather than guessed.
	if got := ParseRating("x/10"); got.Status != RatingNone {
		t.Errorf("x/10 = %+v", got)
	}
}

func TestComposeFallsBackToSpanText(t *testing.T) {
	// A span whose offsets are out of range is still rendered from its own text, so a corrupt row cannot panic.
	got := Compose("short", []Span{{Start: 100, End: 200, Text: "kept"}})
	if got != "kept" {
		t.Errorf("Compose = %q", got)
	}
}

func TestContainedInBlank(t *testing.T) {
	if ContainedIn("anything", "   ") {
		t.Error("blank text was reported as contained")
	}
	if ContainedIn("anything", "") {
		t.Error("empty text was reported as contained")
	}
}

func TestMergeSortsConflicts(t *testing.T) {
	// Three sources disagreeing about the same field, with the same provider name twice: the sort must still be
	// deterministic, which is what makes a re-merge compare equal.
	a := Source{Provider: "one", Trust: TrustVerified, Title: "Dune", Details: map[string]string{"pages": "412"}}
	b := Source{Provider: "two", Trust: TrustVerified, Title: "Dune", Details: map[string]string{"pages": "400"}}
	c := Source{Provider: "two", Trust: TrustVerified, Title: "Dune", Details: map[string]string{"pages": "390"}}

	first := Merge("book", []Source{a, b, c})
	second := Merge("book", []Source{a, c, b})
	if len(first.Conflicts) != len(second.Conflicts) {
		t.Fatalf("conflicts differ in length: %d vs %d", len(first.Conflicts), len(second.Conflicts))
	}
	for i := range first.Conflicts {
		if first.Conflicts[i] != second.Conflicts[i] {
			t.Errorf("conflict order depends on input order: %+v vs %+v", first.Conflicts, second.Conflicts)
			break
		}
	}
}

func TestMergeIgnoresAnEmptyDetail(t *testing.T) {
	a := Source{Provider: "tmdb", Trust: TrustVerified, Title: "Dune", Details: map[string]string{"runtime": ""}}
	got := Merge("film", []Source{a})
	if _, ok := got.Work.Details["runtime"]; ok {
		t.Error("an empty detail became canonical")
	}
}

func TestMergeSkipsUntitledSources(t *testing.T) {
	a := Source{Provider: "tmdb", Trust: TrustVerified, Title: "   "}
	b := Source{Provider: "openlibrary", Trust: TrustVerified, Title: "Dune"}
	got := Merge("book", []Source{a, b})
	if got.Work.Title != "Dune" {
		t.Errorf("title = %q, want the only real title", got.Work.Title)
	}
}

func TestFormatCuesOverTheLimit(t *testing.T) {
	if got := FormatCues(strings.Repeat("cinema ", maxDateScan)); len(got) != 0 {
		t.Errorf("an over-limit message produced cues: %v", got)
	}
}

func TestDiffFavourite(t *testing.T) {
	e := extractedEntry(t)
	after := e
	after.Favourite = true
	changes := Diff(e, after)
	if len(changes) != 1 || changes[0].Field != "favourite" || changes[0].After != "true" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestLocateOriginalRangeGuards(t *testing.T) {
	n := newNormalised("hello world")
	if start, end := n.originalRange("hello world", 3, 3); start != 0 || end != 0 {
		t.Errorf("an empty range gave %d:%d", start, end)
	}
	if start, end := n.originalRange("hello world", 99, 120); start != 0 || end != 0 {
		t.Errorf("an out-of-range slice gave %d:%d", start, end)
	}
}

func TestSimilarityOfEmptyStrings(t *testing.T) {
	if got := similarity("", ""); got != 0 {
		t.Errorf("similarity(\"\", \"\") = %v, want 0", got)
	}
}

func TestSegmentGuards(t *testing.T) {
	if _, ok := segment("short", 2, 99); ok {
		t.Error("an out-of-range segment was accepted")
	}
	if _, ok := segment("short", 3, 3); ok {
		t.Error("an empty segment was accepted")
	}
}

func TestSpansContainedEmpty(t *testing.T) {
	if SpansContained("source", nil) {
		t.Error("no spans was reported as contained")
	}
}

func TestSameWorkIgnoresEmptyIDs(t *testing.T) {
	a := WorkIdentity{Medium: "film", Title: "Dune", ProviderIDs: map[string]string{"tmdb": ""}, ExternalIDs: map[string]string{"imdb": ""}}
	b := WorkIdentity{Medium: "film", Title: "Blade Runner", ProviderIDs: map[string]string{"tmdb": ""}}
	if got := SameWork(a, b); got != SameWorkDifferent {
		t.Errorf("SameWork = %q; an empty id must not join two works", got)
	}
}

func TestResolveDayMonthImpossibleWithoutAYear(t *testing.T) {
	today := time.Date(2026, time.October, 30, 0, 0, 0, 0, london)
	if got := ResolveDate("finished it on 31 February", today, london); got.Confidence == DateClear {
		t.Errorf("31 February resolved to %v", got.Date)
	}
}

func TestMergeConflictTieBreakers(t *testing.T) {
	// Same field, different providers, same rejected value: the sort falls through to the provider name.
	a := Source{Provider: "p1", Trust: TrustVerified, Title: "Dune", Details: map[string]string{"runtime": "155"}}
	b := Source{Provider: "p2", Trust: TrustVerified, Title: "Dune", Details: map[string]string{"runtime": "156"}}
	c := Source{Provider: "p3", Trust: TrustVerified, Title: "Dune", Details: map[string]string{"runtime": "156"}}

	got := Merge("film", []Source{a, b, c})
	if len(got.Conflicts) != 2 {
		t.Fatalf("conflicts = %+v, want two", got.Conflicts)
	}
	if got.Conflicts[0].From != "p2" || got.Conflicts[1].From != "p3" {
		t.Errorf("conflicts are not sorted by provider: %+v", got.Conflicts)
	}
}

// Unicode digits, including the mathematical sets whose blocks abut. The fuzzer found a panic here: counting back
// to a block start returned 10 for a digit in the second of two adjacent sets.
func TestUnicodeDigits(t *testing.T) {
	cases := map[string]int{
		"4.5": 18,
		"۵":   20, // ARABIC-INDIC DIGIT FIVE
		"४.५": 18, // DEVANAGARI
		"４.５": 18, // FULLWIDTH
		"𝟹":   12, // MATHEMATICAL DOUBLE-STRUCK THREE, in an abutting block
		"𝟺/𝟻": 16, // MATHEMATICAL DOUBLE-STRUCK FOUR OF FIVE
		"𐒤/𐒥": 16, // OSMANYA four of five
	}
	for in, want := range cases {
		got := ParseRating(in)
		if got.Quarters != want {
			t.Errorf("ParseRating(%q) = %+v, want %d quarters", in, got, want)
		}
	}
}

// A cost summary is two numbers with a rule: covered is part of the total, never in addition to it, and a total that
// includes a price-list guess says so. Adding two calls is how a session's receipt is built.
func TestCostSummarySplitsCoveredAndSpent(t *testing.T) {
	providerReported := CostOf(0.0002, 0, CostSourceProvider)
	if providerReported.Spent() != 0.0002 || providerReported.Estimated {
		t.Errorf("provider cost = %+v", providerReported)
	}
	covered := CostOf(0.003, 0.003, CostSourceAllowance)
	if covered.Spent() != 0 || covered.Covered != 0.003 {
		t.Errorf("covered cost = %+v, want nothing spent", covered)
	}
	guessed := CostOf(0.004, 0, CostSourceCatalog)
	if !guessed.Estimated {
		t.Error("a catalog price was not marked as an estimate")
	}

	sum := providerReported.Add(covered).Add(guessed)
	if sum.Total != 0.0072 || sum.Covered != 0.003 {
		t.Errorf("sum = %+v", sum)
	}
	if !sum.Estimated {
		t.Error("a total that includes an estimate did not say so")
	}
	if spent := sum.Spent(); spent < 0.00419 || spent > 0.00421 {
		t.Errorf("spent = %v, want the uncovered 0.0042", spent)
	}
	// Nothing plus nothing is zero, which is what a session with no calls reports.
	if empty := (CostSummary{}).Add(CostSummary{}); empty.Total != 0 || empty.Covered != 0 || empty.Estimated {
		t.Errorf("empty sum = %+v", empty)
	}
}

// A ledger row turns into a summary the same way whatever kind of call it was, so a screen never reads one ledger
// differently from the other.
func TestLedgerRowsCarryTheirCostProvenance(t *testing.T) {
	call := DecisionCall{CostUSD: 0.002, AllowanceUSD: 0.0015, CostSource: CostSourceAllowance}
	if got := call.Cost(); got.Total != 0.002 || got.Covered != 0.0015 || got.Estimated {
		t.Errorf("decision cost = %+v", got)
	}
	turn := TurnCall{CostUSD: 0.004, CostSource: CostSourceCatalog}
	if got := turn.Cost(); got.Total != 0.004 || got.Covered != 0 || !got.Estimated {
		t.Errorf("turn cost = %+v", got)
	}
	if got := (TurnCall{}).Cost(); got.Total != 0 || got.Covered != 0 || got.Estimated {
		t.Errorf("empty turn cost = %+v", got)
	}
}
