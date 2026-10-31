package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzLocate feeds arbitrary sources and quotes to the locator. The invariants are the ones a saved body depends
// on, so a fuzz failure is a bug in the guarantee, not a crash to be papered over.
func FuzzLocate(f *testing.F) {
	seeds := [][3]string{
		{"hey so i finished watching dune last night, loved it honestly, easy 4.5", "loved it honestly, easy 4.5", "the world felt so lived in"},
		{"a b c", "a", "c"},
		{"", "", ""},
		{"one\ntwo\nthree", "one two", "three"},
		{"café au lait", "cafe au lait", "café"},
		{"★★★★", "★★", "★★★★★"},
		{strings.Repeat("x", 100), "x", "xx"},
		{"it's a test", "its a test", "it's"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1], s[2])
	}

	f.Fuzz(func(t *testing.T, source, a, b string) {
		if len(source) > MaxSpanSource {
			return // over the limit is a refusal with its own test
		}
		got := Locate(source, []string{a, b})

		prevEnd := 0
		for _, span := range got.Spans {
			if span.Start < 0 || span.End > len(source) || span.Start >= span.End {
				t.Fatalf("span out of range: %+v (source %d bytes)", span, len(source))
			}
			if !utf8.ValidString(span.Text) {
				t.Fatalf("span text is not valid UTF-8: %q", span.Text)
			}
			if source[span.Start:span.End] != span.Text {
				t.Fatalf("span text %q is not source[%d:%d] (%q)", span.Text, span.Start, span.End, source[span.Start:span.End])
			}
			if span.Start < prevEnd {
				t.Fatalf("spans overlap or are unordered: %+v", got.Spans)
			}
			prevEnd = span.End
			if !ContainedIn(source, span.Text) {
				t.Fatalf("kept span %q is not contained in the source", span.Text)
			}
		}
	})
}

// FuzzParseRating feeds arbitrary text to the rating parser: it must never panic, never return an out-of-range
// value, and never return a value without a status.
func FuzzParseRating(f *testing.F) {
	for _, seed := range []string{"4.5", "8/10", "four stars", "a four and a bit", "", "★★★★★", "۴.۵", "0/10", "99"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		got := ParseRating(text)
		switch got.Status {
		case RatingNone, RatingVague:
			if got.Quarters != 0 {
				t.Fatalf("status %q carried a value: %+v", got.Status, got)
			}
		case RatingStated, RatingSnapped:
			if !ValidRating(got.Quarters) {
				t.Fatalf("status %q carried an out-of-range value: %+v", got.Status, got)
			}
			if FormatRating(got.Quarters) == "" {
				t.Fatalf("a valid rating did not format: %+v", got)
			}
		default:
			t.Fatalf("unknown status %q", got.Status)
		}
	})
}
