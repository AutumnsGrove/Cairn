package domain

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The span locator is the proof of invariant 1: every saved character came from the operator's own message. These
// tests cover the happy route (a clean quote), the bad route (an invented quote, a lookalike, a huge input) and
// the sad route (a pathological input, a cancelled locate).

const message = "hey so i finished watching dune last night, loved it honestly, easy 4.5, " +
	"the world felt so lived in but the middle dragged a bit. would watch again."

func TestLocateExact(t *testing.T) {
	quote := "loved it honestly, easy 4.5"
	got := Locate(message, []string{quote})

	if len(got.Spans) != 1 {
		t.Fatalf("spans = %+v, want exactly one", got.Spans)
	}
	span := got.Spans[0]
	if span.Text != quote {
		t.Errorf("span text = %q, want the exact wording %q", span.Text, quote)
	}
	if message[span.Start:span.End] != span.Text {
		t.Errorf("offsets %d:%d do not cover the span text", span.Start, span.End)
	}
}

// A slip in case, punctuation or spacing still lands, and the stored text is what the operator actually wrote.
func TestLocateSnapsToTheOriginalWording(t *testing.T) {
	cases := map[string]string{
		"Loved it honestly easy 4.5":                              "loved it honestly, easy 4.5",
		"the world felt so lived in":                              "the world felt so lived in",
		"middle   dragged":                                        "middle dragged",
		"would watch again.":                                      "would watch again.",
		"the world felt so lived in but the middle dragged a bit": "the world felt so lived in but the middle dragged a bit",
	}
	for quote, want := range cases {
		got := Locate(message, []string{quote})
		if len(got.Spans) != 1 {
			t.Errorf("Locate(%q) = %d spans, want 1 (left out %v)", quote, len(got.Spans), got.LeftOut)
			continue
		}
		if got.Spans[0].Text != want {
			t.Errorf("Locate(%q) stored %q, want the original %q", quote, got.Spans[0].Text, want)
		}
	}
}

// A one-word slip is the case the closest-passage search exists for.
func TestLocateClosestPassage(t *testing.T) {
	got := Locate(message, []string{"loved it honestly, easy 4.75"})
	if len(got.Spans) != 1 {
		t.Fatalf("spans = %+v, want one after a one-token slip", got.Spans)
	}
	if !strings.Contains(got.Spans[0].Text, "loved it honestly") {
		t.Errorf("span = %q, want the operator's own sentence", got.Spans[0].Text)
	}
}

func TestLocateTwoQuotesInOrder(t *testing.T) {
	got := Locate(message, []string{
		"the middle dragged a bit",
		"loved it honestly, easy 4.5",
	})
	if len(got.Spans) != 2 {
		t.Fatalf("spans = %+v, want two", got.Spans)
	}
	if got.Spans[0].Start >= got.Spans[1].Start {
		t.Errorf("spans are not in source order: %+v", got.Spans)
	}
}

func TestLocateMergesOverlaps(t *testing.T) {
	got := Locate(message, []string{
		"loved it honestly, easy 4.5",
		"easy 4.5, the world felt so lived in",
	})
	if len(got.Spans) != 1 {
		t.Fatalf("spans = %+v, want the overlap merged into one", got.Spans)
	}
	if !strings.Contains(got.Spans[0].Text, "easy 4.5") {
		t.Errorf("merged span = %q", got.Spans[0].Text)
	}
}

// Everything not kept is left out, so the card can offer it back with one tap.
func TestLeftOutIsTheRest(t *testing.T) {
	got := Locate(message, []string{"the world felt so lived in"})
	if len(got.LeftOut) == 0 {
		t.Fatal("nothing left out, want the framing")
	}
	var left strings.Builder
	for _, seg := range got.LeftOut {
		left.WriteString(seg.Text)
	}
	if !strings.Contains(left.String(), "hey so i finished watching dune") {
		t.Errorf("left out = %q, want the framing", left.String())
	}
	if strings.Contains(left.String(), "the world felt so lived in") {
		t.Errorf("left out contains the kept span: %q", left.String())
	}
	for _, seg := range got.LeftOut {
		if message[seg.Start:seg.End] != seg.Text {
			t.Errorf("left-out segment offsets are wrong: %+v", seg)
		}
		if strings.TrimSpace(seg.Text) == "" {
			t.Errorf("a whitespace-only segment was reported as left out: %+v", seg)
		}
	}
}

// Bad: a quote the operator never wrote is dropped, never stored. This is the rule the whole package exists for.
func TestLocateDropsInventedQuotes(t *testing.T) {
	got := Locate(message, []string{
		"a masterclass in restrained world-building",
		"the cinematography is an absolute triumph",
	})
	if len(got.Spans) != 0 {
		t.Fatalf("spans = %+v, want none", got.Spans)
	}
	if len(got.Dropped) != 2 {
		t.Errorf("dropped = %v, want both quotes reported for logging", got.Dropped)
	}
}

// A homoglyph is a slip the closest-passage search is for: a model reading a screenshot may return a Cyrillic "е".
// Matching is allowed; storing the lookalike is not. The saved text must be the source's own wording.
func TestLocateLookalikeStoresTheOriginal(t *testing.T) {
	source := "i finally read Dune and it was dense"
	lookalike := "i finally read Dun\u0435" // Cyrillic small letter ie
	got := Locate(source, []string{lookalike})
	if len(got.Spans) != 1 {
		t.Fatalf("spans = %+v, want the passage found", got.Spans)
	}
	if got.Spans[0].Text != "i finally read Dune" {
		t.Errorf("stored %q, want the source's own wording", got.Spans[0].Text)
	}
	if strings.ContainsRune(got.Spans[0].Text, '\u0435') {
		t.Error("the lookalike character was stored")
	}
}

// Bad: a short quote may not be matched approximately, because one edit turns one word into another.
func TestLocateShortQuotesNeedAnExactMatch(t *testing.T) {
	source := "the ending was done well enough"
	if got := Locate(source, []string{"dune"}); len(got.Spans) != 0 {
		t.Fatalf("a short quote matched approximately: %+v", got.Spans)
	}
	// The same source, the real word: an exact match still lands.
	if got := Locate(source, []string{"done well"}); len(got.Spans) != 1 {
		t.Fatalf("an exact short quote did not match: %+v", got.Spans)
	}
}

// Bad: a quote with nothing like it in the source is dropped rather than stretched onto a nearby sentence.
func TestLocateDoesNotStretch(t *testing.T) {
	got := Locate(message, []string{"a devastating portrait of empire"})
	if len(got.Spans) != 0 {
		t.Fatalf("spans = %+v, want none: a weak match must be refused", got.Spans)
	}
}

// A span that covers a line break keeps the break, because the stored text is the original.
func TestLocateSpanAcrossANewline(t *testing.T) {
	source := "finished it last night\nloved the ending\nthe middle dragged"
	got := Locate(source, []string{"last night loved the ending"})
	if len(got.Spans) != 1 {
		t.Fatalf("spans = %+v, want one", got.Spans)
	}
	if !strings.Contains(got.Spans[0].Text, "\n") {
		t.Errorf("span = %q, want the original line break kept", got.Spans[0].Text)
	}
}

func TestLocateEmptyInputs(t *testing.T) {
	if got := Locate("", []string{"anything"}); len(got.Spans) != 0 {
		t.Errorf("empty source produced spans: %+v", got)
	}
	if got := Locate(message, nil); len(got.Spans) != 0 {
		t.Errorf("no quotes produced spans: %+v", got)
	}
	if got := Locate(message, []string{"", "   "}); len(got.Spans) != 0 {
		t.Errorf("blank quotes produced spans: %+v", got)
	}
}

// Bad: too many quotes are capped, not silently all kept, and the cap is the documented one.
func TestLocateCapsQuoteCount(t *testing.T) {
	quotes := make([]string, MaxSpansPerBody+10)
	for i := range quotes {
		quotes[i] = "loved it honestly"
	}
	got := Locate(message, quotes)
	if len(got.Spans) > MaxSpansPerBody {
		t.Fatalf("%d spans, want at most %d", len(got.Spans), MaxSpansPerBody)
	}
}

// Bad: an enormous source is refused in bounded time rather than scanned forever.
func TestLocateHugeSourceIsBounded(t *testing.T) {
	huge := strings.Repeat("the middle dragged a bit. ", 400_000) // about 10 MB
	start := time.Now()
	got := Locate(huge, []string{"the middle dragged a bit"})
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("Locate took %v on a 10 MB source", elapsed)
	}
	if len(got.Spans) != 0 {
		t.Errorf("a source over the limit produced %d spans; it should be refused, not truncated", len(got.Spans))
	}
}

// Sad: a pathological input (a long source, the maximum quotes, each nearly as long as the source) still returns.
func TestLocatePathologicalInputIsBounded(t *testing.T) {
	source := strings.Repeat("abc ", MaxSpanSource/4)
	quotes := make([]string, MaxSpansPerBody)
	for i := range quotes {
		quotes[i] = strings.Repeat("abc ", 400)
	}
	start := time.Now()
	Locate(source, quotes)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Locate took %v on a pathological input", elapsed)
	}
}

// Sad: a cancelled locate returns promptly and does not claim success.
func TestLocateHonoursCancellation(t *testing.T) {
	source := strings.Repeat("abc ", MaxSpanSource/4)
	quotes := make([]string, MaxSpansPerBody)
	for i := range quotes {
		quotes[i] = strings.Repeat("abc ", 400)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	got := LocateContext(ctx, source, quotes)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("a cancelled locate took %v", elapsed)
	}
	if len(got.Spans) != 0 {
		t.Errorf("a cancelled locate returned spans: %+v", got.Spans)
	}
}

// --- composing and containment -------------------------------------------------------------------------

func TestCompose(t *testing.T) {
	source := "first sentence. second sentence. third sentence."
	spans := []Span{
		{Start: 0, End: 15, Text: "first sentence."},
		{Start: 16, End: 32, Text: "second sentence."},
	}
	if got := Compose(source, spans); got != "first sentence.\nsecond sentence." {
		t.Errorf("Compose = %q", got)
	}
	if got := Compose(source, nil); got != "" {
		t.Errorf("Compose(nil) = %q, want empty", got)
	}
}

func TestContainedIn(t *testing.T) {
	if !ContainedIn(message, "loved it honestly, easy 4.5") {
		t.Error("an exact substring was not contained")
	}
	if !ContainedIn(message, "Loved it honestly easy 4.5") {
		t.Error("a case and punctuation slip was not contained")
	}
	if ContainedIn(message, "a masterpiece of modern cinema") {
		t.Error("invented text was reported as contained")
	}
	if ContainedIn("", "anything") {
		t.Error("empty source contained text")
	}
}

func TestSpansContained(t *testing.T) {
	spans := []Span{{Start: 10, End: 20, Text: message[10:20]}}
	if !SpansContained(message, spans) {
		t.Error("a true span was not contained")
	}
	bad := []Span{{Start: 10, End: 20, Text: "not the source"}}
	if SpansContained(message, bad) {
		t.Error("a span whose text differs from the source was accepted")
	}
	if SpansContained(message, []Span{{Start: 0, End: 9999, Text: "x"}}) {
		t.Error("an out-of-range span was accepted")
	}
}

func TestLocateVerdict(t *testing.T) {
	span, ok := LocateVerdict(message, "would watch again")
	if !ok {
		t.Fatal("a real verdict was not located")
	}
	if span.Text != "would watch again" {
		t.Errorf("verdict = %q", span.Text)
	}
	if _, ok := LocateVerdict(message, "the best film of the decade"); ok {
		t.Error("an invented verdict was accepted")
	}
	long := strings.Repeat("very ", 100)
	if _, ok := LocateVerdict(message+" "+long, long); ok {
		t.Error("a verdict over the length limit was accepted")
	}
}

func TestNormaliseForMatching(t *testing.T) {
	cases := map[string]string{
		"Hello, World!":    "hello world",
		"  spaced   out  ": "spaced out",
		"it's a test":      "its a test",
		"dune (2021)":      "dune 2021",
		"em—dash and…":     "emdash and",
		"café":             "café", // accents are kept: they change the word
	}
	for in, want := range cases {
		if got := normaliseForMatching(in); got != want {
			t.Errorf("normaliseForMatching(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComplementIsTheRestOfTheSource(t *testing.T) {
	source := "hey so i finished dune last night, the world felt lived in, the middle dragged a bit"
	start := strings.Index(source, "the world")
	spans := []Span{{Start: start, End: start + len("the world felt lived in"), Text: source[start : start+len("the world felt lived in")]}}

	left := Complement(source, spans)
	if len(left) != 2 {
		t.Fatalf("left out = %v, want the framing before and after", left)
	}
	for _, seg := range left {
		if strings.TrimSpace(seg.Text) == "" {
			t.Errorf("a whitespace-only gap was kept: %+v", seg)
		}
	}
	if got := source[left[0].Start:left[0].End]; got != left[0].Text {
		t.Errorf("segment text is not the source: %q", got)
	}
	if len(Complement(source, nil)) != 1 {
		t.Error("with no spans the whole source is left out")
	}
	if len(Complement(source, []Span{{Start: -1, End: 2, Text: "he"}})) != 0 {
		t.Error("a damaged span should yield no left-out text")
	}
}
