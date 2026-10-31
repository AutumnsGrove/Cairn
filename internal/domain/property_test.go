package domain

import (
	"strings"
	"testing"
	"testing/quick"
)

// The properties that must hold for every input, not just the ones someone thought of. These are the assertions
// that would catch a clever optimisation breaking invariant 1.

func TestPropertySpansAreTrueSubstrings(t *testing.T) {
	f := func(source, a, b string) bool {
		got := Locate(source, []string{a, b})
		for _, span := range got.Spans {
			if span.Start < 0 || span.End > len(source) || span.Start >= span.End {
				return false
			}
			if source[span.Start:span.End] != span.Text {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

func TestPropertySpansAreOrderedAndDisjoint(t *testing.T) {
	f := func(source, a, b string) bool {
		spans := Locate(source, []string{a, b}).Spans
		for i := 1; i < len(spans); i++ {
			if spans[i].Start < spans[i-1].End {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

// Compose may add a newline between passages, and nothing else: remove those and the body is a subsequence of the
// source, which is the weakest form of "my words are mine" that still allows several passages.
func TestPropertyComposeIsASubsequence(t *testing.T) {
	f := func(source, a, b string) bool {
		got := Locate(source, []string{a, b})
		body := strings.ReplaceAll(Compose(source, got.Spans), "\n", "")
		return isSubsequence(body, source)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

// Every kept span is contained in the source by the same test the save guard uses.
func TestPropertySpansAreContained(t *testing.T) {
	f := func(source, quote string) bool {
		for _, span := range Locate(source, []string{quote}).Spans {
			if !ContainedIn(source, span.Text) {
				return false
			}
			if !SpansContained(source, []Span{span}) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

// Left out plus kept covers the source, with no overlap and nothing invented.
func TestPropertyLeftOutAndKeptPartition(t *testing.T) {
	f := func(source, quote string) bool {
		got := Locate(source, []string{quote})
		covered := make([]bool, len(source))
		for _, span := range got.Spans {
			for i := span.Start; i < span.End; i++ {
				covered[i] = true
			}
		}
		for _, seg := range got.LeftOut {
			if seg.Start < 0 || seg.End > len(source) || seg.Start >= seg.End {
				return false
			}
			if source[seg.Start:seg.End] != seg.Text {
				return false
			}
			for i := seg.Start; i < seg.End; i++ {
				if covered[i] {
					return false
				}
				covered[i] = true
			}
		}
		// Only whitespace may be uncovered: everything else is either kept or reported as left out.
		for i, c := range covered {
			if !c && source[i] != ' ' && source[i] != '\n' && source[i] != '\t' && source[i] != '\r' {
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

func isSubsequence(needle, hay string) bool {
	i := 0
	for j := 0; j < len(hay) && i < len(needle); j++ {
		if needle[i] == hay[j] {
			i++
		}
	}
	return i == len(needle)
}
