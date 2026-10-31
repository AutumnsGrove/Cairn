package domain

import (
	"strings"
	"unicode"
)

// Titles are how a work is recognised when no provider id joins the sources. The comparison is deliberately
// conservative: a wrong merge is worse than a duplicate (PLAN.md 7), so anything uncertain comes back as "ask".

// WorkIdentity is everything known about a work from one source, reduced to what a same-work decision needs.
type WorkIdentity struct {
	Medium      string            `json:"medium"`
	Title       string            `json:"title"`
	Year        int               `json:"year"`
	Creator     string            `json:"creator"`
	ProviderIDs map[string]string `json:"provider_ids,omitempty"` // provider name to that provider's id
	ExternalIDs map[string]string `json:"external_ids,omitempty"` // imdb, isbn, musicbrainz, igdb
}

// SameWorkDecision is the answer to "is this the same work?".
type SameWorkDecision string

const (
	// SameWorkSame means the two identities are the same work.
	SameWorkSame SameWorkDecision = "same"
	// SameWorkDifferent means they are certainly different works.
	SameWorkDifferent SameWorkDecision = "different"
	// SameWorkAsk means they are close enough that the operator should choose.
	SameWorkAsk SameWorkDecision = "ask"
)

const (
	// titleSame is the similarity at or above which two titles are the same title.
	titleSame = 0.99
	// titleAsk is the similarity at or above which two titles are close enough to ask about.
	titleAsk = 0.75
)

// SameWork decides whether two identities describe one work. Provider ids win, then external ids, then the
// normalised (title, year, creator) triple, and a near miss is an ask rather than a guess.
func SameWork(a, b WorkIdentity) SameWorkDecision {
	// A provider id is stable: if both sources came from the same provider, its id settles it.
	for provider, id := range a.ProviderIDs {
		if id == "" {
			continue
		}
		if other, ok := b.ProviderIDs[provider]; ok && other == id {
			return SameWorkSame
		}
	}

	// An external id is the join key across providers: the same ISBN is the same book.
	for name, id := range a.ExternalIDs {
		if id == "" {
			continue
		}
		if other, ok := b.ExternalIDs[name]; ok && other == id {
			return SameWorkSame
		}
	}

	// Two different media are two different works, even with the same title: a film adaptation is its own work.
	if a.Medium != "" && b.Medium != "" && !strings.EqualFold(a.Medium, b.Medium) {
		return SameWorkDifferent
	}

	sim := TitleSimilarity(a.Title, b.Title)
	switch {
	case sim >= titleSame:
		if yearCompatible(a.Year, b.Year) && creatorCompatible(a.Creator, b.Creator) {
			return SameWorkSame
		}
		return SameWorkAsk
	case sim >= titleAsk:
		return SameWorkAsk
	default:
		return SameWorkDifferent
	}
}

// yearCompatible is true when the years agree or only one source knows one. A known disagreement is not
// compatible: it is exactly the case worth asking about.
func yearCompatible(a, b int) bool {
	return a == 0 || b == 0 || a == b
}

// creatorCompatible compares primary creators by normalised name. An empty creator on either side is not a
// disagreement, only an absence.
func creatorCompatible(a, b string) bool {
	na, nb := normaliseName(a), normaliseName(b)
	if na == "" || nb == "" {
		return true
	}
	return na == nb
}

// NormaliseTitle reduces a title to what two sources can agree on: lower case, no leading article, no
// punctuation, single spaces. Accents stay, because they are part of the title.
func NormaliseTitle(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	var b strings.Builder
	b.Grow(len(title))
	lastSpace := true
	for _, r := range title {
		switch {
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case r == '&':
			// "Pride & Prejudice" and "Pride and Prejudice" are one title.
			if !lastSpace {
				b.WriteByte(' ')
			}
			b.WriteString("and ")
			lastSpace = true
		}
	}
	return stripLeadingArticle(strings.TrimSpace(b.String()))
}

// stripLeadingArticle removes "the", "a" or "an" from the front, so "The Matrix" and "Matrix" agree.
func stripLeadingArticle(s string) string {
	for _, article := range []string{"the ", "an ", "a "} {
		if rest, ok := strings.CutPrefix(s, article); ok {
			return rest
		}
	}
	return s
}

// normaliseName reduces a creator's name for comparison, ignoring punctuation and extra spaces.
func normaliseName(name string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// TitleSimilarity is 1 for identical normalised titles and falls off with edit distance. It is a starting value's
// measure: S08 tunes the thresholds against real candidates (gate G4).
func TitleSimilarity(a, b string) float64 {
	na, nb := NormaliseTitle(a), NormaliseTitle(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 1
	}
	longest := max(len(na), len(nb))
	d, ok := boundedDistance(na, nb, longest)
	if !ok {
		return 0
	}
	return 1 - float64(d)/float64(longest)
}
