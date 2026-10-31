package domain

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on the span locator. They are in docs/STANDARDS.md 4a; a value over a limit has a defined outcome here
// and a test beside it.
const (
	// MaxSpansPerBody is how many quotes one body may keep. Extra quotes are dropped and reported.
	MaxSpansPerBody = 40
	// MaxSpanSource is the largest source text the locator will search. A bigger one is refused, never clipped.
	MaxSpanSource = 64 << 10
	// MaxQuoteLength is the longest quote considered. A longer one is dropped.
	MaxQuoteLength = 2000
	// MaxVerdictLength is the longest verdict. The verdict is one line.
	MaxVerdictLength = 200
	// matchCutoff is how close a passage must be to count as the quote. A starting value, tuned from the
	// fixture corpus in S08 (gate G4).
	matchCutoff = 0.72
	// fuzzyQuoteLimit is the longest quote that gets the expensive closest-passage search. Beyond it, only an
	// exact or normalised match counts, which keeps a pathological input bounded.
	fuzzyQuoteLimit = 600
	// minFuzzyLength is the shortest quote that may be matched approximately. Below it a single edit can turn one
	// word into a different one ("dune" and "done" are one edit apart), so short quotes must match exactly.
	minFuzzyLength = 12
	// maxAnchors, maxOccurrences and maxCandidates bound the closest-passage search.
	maxAnchors     = 4
	maxOccurrences = 4
	maxCandidates  = 24
)

// Span is a half-open byte range in the source text, with the exact text it covers.
type Span struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// Segment is a piece of the source that was not kept, so the card can offer it back.
type Segment struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// Located is the outcome of locating an agent's quotes in the operator's own text.
type Located struct {
	// Spans are the kept passages, in source order, with overlaps merged. Their text is the original wording.
	Spans []Span
	// LeftOut is everything else in the source, minus whitespace-only gaps.
	LeftOut []Segment
	// Dropped is the quotes that had no close-enough passage. They are for the log, never for the body.
	Dropped []string
}

// Locate finds the operator's own wording for each quote. A quote that is not there is dropped, because a saved
// body may only contain text that exists in the source (docs/BUILD.md invariant 1).
func Locate(source string, quotes []string) Located {
	return LocateContext(context.Background(), source, quotes)
}

// LocateContext is Locate with a context, so a long search can be abandoned. The function is pure; the context
// is only a way to stop early.
func LocateContext(ctx context.Context, source string, quotes []string) Located {
	if source == "" || !utf8.ValidString(source) || len(source) > MaxSpanSource {
		return Located{}
	}
	if len(quotes) > MaxSpansPerBody {
		quotes = quotes[:MaxSpansPerBody]
	}

	norm := newNormalised(source)
	var out Located

	for _, quote := range quotes {
		if ctx.Err() != nil {
			return Located{}
		}
		quote = strings.TrimSpace(quote)
		if quote == "" || !utf8.ValidString(quote) || len(quote) > MaxQuoteLength {
			if quote != "" {
				out.Dropped = append(out.Dropped, quote)
			}
			continue
		}
		if span, ok := norm.exact(source, quote, cursorOf(out.Spans)); ok {
			out.Spans = append(out.Spans, span)
			continue
		}
		if span, ok := norm.closest(source, quote); ok {
			out.Spans = append(out.Spans, span)
			continue
		}
		out.Dropped = append(out.Dropped, quote)
	}

	out.Spans = mergeSpans(source, out.Spans)
	out.LeftOut = complement(source, out.Spans)
	return out
}

// LocateVerdict locates the single short quote used as the verdict. It goes through the same containment rule, so
// a verdict the operator did not write stays empty.
func LocateVerdict(source, quote string) (Span, bool) {
	quote = strings.TrimSpace(quote)
	if quote == "" || len(quote) > MaxVerdictLength {
		return Span{}, false
	}
	got := Locate(source, []string{quote})
	if len(got.Spans) != 1 {
		return Span{}, false
	}
	return got.Spans[0], true
}

// Compose builds the body from spans in source order. Adjacent spans join with a single newline, which is the only
// character Compose ever adds; each span's own text is untouched.
func Compose(source string, spans []Span) string {
	var b strings.Builder
	for i, span := range spans {
		if i > 0 {
			b.WriteByte('\n')
		}
		if span.Start >= 0 && span.End <= len(source) && span.Start <= span.End {
			b.WriteString(source[span.Start:span.End])
			continue
		}
		b.WriteString(span.Text)
	}
	return b.String()
}

// ContainedIn reports whether text is present in source, ignoring case, spacing and punctuation. It is the proof
// the save guard asks for: a body made of extracted words must be contained in the operator's messages.
func ContainedIn(source, text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	needle := normaliseForMatching(text)
	if needle == "" {
		// Nothing survived normalisation, which means the text is made only of symbols: compare it as written.
		return strings.Contains(source, text)
	}
	return strings.Contains(normaliseForMatching(source), needle)
}

// SpansContained reports whether every span still matches the source at its recorded offsets. This is the check
// used on a saved entry, where the spans are re-read from storage.
func SpansContained(source string, spans []Span) bool {
	if len(spans) == 0 {
		return false
	}
	for _, span := range spans {
		if span.Start < 0 || span.End > len(source) || span.Start >= span.End {
			return false
		}
		if source[span.Start:span.End] != span.Text {
			return false
		}
	}
	return true
}

// normaliseForMatching lowers case, collapses whitespace, and drops punctuation and symbols. Accents are kept,
// because "café" and "cafe" are different words; the locator must not quietly change what someone wrote.
func normaliseForMatching(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := true // so leading whitespace is trimmed without a second pass
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		default:
			// Punctuation, symbols and marks are dropped for matching only.
		}
	}
	return strings.TrimSpace(b.String())
}

// normalised is the source with a map back to original byte offsets, so a match can be turned into the exact
// wording that was written.
type normalised struct {
	text   string
	origin []int // origin[i] is the original byte offset of normalised byte i
}

func newNormalised(source string) normalised {
	var b strings.Builder
	b.Grow(len(source))
	origin := make([]int, 0, len(source))

	lastSpace := true
	for i, r := range source {
		switch {
		case unicode.IsSpace(r):
			if lastSpace {
				continue
			}
			b.WriteByte(' ')
			origin = append(origin, i)
			lastSpace = true
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			lower := strings.ToLower(string(r))
			b.WriteString(lower)
			// One origin entry per BYTE written, not per rune: a multi-byte lower-cased rune would otherwise
			// shift every later offset and turn a real span into the wrong text.
			for k := 0; k < len(lower); k++ {
				origin = append(origin, i)
			}
			lastSpace = false
		}
	}
	return normalised{text: b.String(), origin: origin}
}

// exact tries the source as written first, then the normalised form. The written match wins because it needs no
// mapping, and both return the operator's exact wording.
func (n normalised) exact(source, quote string, cursor int) (Span, bool) {
	if span, ok := findExact(source, quote, cursor); ok {
		return span, true
	}
	needle := normaliseForMatching(quote)
	if needle == "" {
		return Span{}, false
	}
	at := strings.Index(n.text, needle)
	if at < 0 {
		return Span{}, false
	}
	start, end := n.originalRange(source, at, at+len(needle))
	if start >= end {
		return Span{}, false
	}
	return Span{Start: start, End: end, Text: source[start:end]}, true
}

// findExact looks for the quote as written, preferring an occurrence at or after the cursor so quotes stay in the
// order they were written, and falling back to the whole source because an agent may quote out of order. An
// occurrence inside a longer word is skipped: "dune" must not match "dunes".
func findExact(source, quote string, cursor int) (Span, bool) {
	if quote == "" {
		return Span{}, false
	}
	passes := []int{cursor, 0}
	for _, from := range passes {
		if from > len(source) {
			continue
		}
		for offset := from; offset <= len(source); {
			at := strings.Index(source[offset:], quote)
			if at < 0 {
				break
			}
			start := offset + at
			end := start + len(quote)
			if onWordBoundary(source, start, end) {
				return Span{Start: start, End: end, Text: source[start:end]}, true
			}
			offset = start + 1
		}
		if cursor == 0 {
			break // the first pass was already the whole source
		}
	}
	return Span{}, false
}

// onWordBoundary rejects a match that starts or ends in the middle of a word.
func onWordBoundary(source string, start, end int) bool {
	if start > 0 {
		before, _ := utf8.DecodeLastRuneInString(source[:start])
		first, _ := utf8.DecodeRuneInString(source[start:])
		if isWordRune(before) && isWordRune(first) {
			return false
		}
	}
	if end < len(source) {
		last, _ := utf8.DecodeLastRuneInString(source[:end])
		after, _ := utf8.DecodeRuneInString(source[end:])
		if isWordRune(last) && isWordRune(after) {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// originalRange maps a normalised byte range back to the original text, trimming whitespace at both ends.
func (n normalised) originalRange(source string, from, to int) (int, int) {
	if from < 0 || to > len(n.origin) || from >= to {
		return 0, 0
	}
	start := n.origin[from]
	end := n.origin[to-1]
	_, size := utf8.DecodeRuneInString(source[end:])
	end += size

	for start < end {
		r, size := utf8.DecodeRuneInString(source[start:])
		if !unicode.IsSpace(r) {
			break
		}
		start += size
	}
	for end > start {
		r, size := utf8.DecodeLastRuneInString(source[start:end])
		if !unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	return start, end
}

// token is a word in the normalised source, with its byte offsets there.
type token struct {
	text  string
	start int
	end   int
}

func tokenise(norm string) []token {
	var out []token
	for i := 0; i < len(norm); {
		if norm[i] == ' ' {
			i++
			continue
		}
		start := i
		for i < len(norm) && norm[i] != ' ' {
			i++
		}
		out = append(out, token{text: norm[start:i], start: start, end: i})
	}
	return out
}

// closest finds the passage that the quote most resembles. It exists for one-word slips and small edits, which an
// exact or normalised match cannot see. The search is bounded at every step: a fixed number of anchors, a fixed
// number of occurrences, and a fixed number of candidates that reach the expensive comparison.
func (n normalised) closest(source, quote string) (Span, bool) {
	needle := normaliseForMatching(quote)
	if len(needle) < minFuzzyLength || len(needle) > fuzzyQuoteLimit {
		return Span{}, false
	}
	qTokens := tokenise(needle)
	if len(qTokens) == 0 {
		return Span{}, false
	}
	srcTokens := tokenise(n.text)
	if len(srcTokens) == 0 || len(qTokens) > len(srcTokens)+2 {
		return Span{}, false
	}

	index := make(map[string][]int, len(srcTokens))
	for i, tok := range srcTokens {
		if len(tok.text) < 4 {
			continue // short words ("the", "and") are everywhere and anchor nothing
		}
		index[tok.text] = append(index[tok.text], i)
	}

	type candidate struct {
		from, to int // token indices, half open
		score    float64
	}
	var candidates []candidate

	// The longest words are the ones worth anchoring on: they are rare, so they pin the passage.
	order := make([]int, len(qTokens))
	for i := range order {
		order[i] = i
	}
	for i := 0; i < len(order) && i < maxAnchors; i++ {
		best := i
		for j := i + 1; j < len(order); j++ {
			if len(qTokens[order[j]].text) > len(qTokens[order[best]].text) {
				best = j
			}
		}
		order[i], order[best] = order[best], order[i]
	}

	for _, anchorIdx := range order[:min(len(order), maxAnchors)] {
		positions := index[qTokens[anchorIdx].text]
		for _, at := range positions[:min(len(positions), maxOccurrences)] {
			// The window is the same length as the quote, shifted by up to two words either way: a slip of a
			// word or two is what this search is for, and a wider window would compare unrelated sentences.
			for shift := -2; shift <= 2; shift++ {
				from := at - anchorIdx + shift
				to := from + len(qTokens)
				if from < 0 || to > len(srcTokens) {
					continue
				}
				score := positionalScore(qTokens, srcTokens[from:to])
				candidates = append(candidates, candidate{from: from, to: to, score: score})
				if len(candidates) >= maxCandidates {
					break
				}
			}
			if len(candidates) >= maxCandidates {
				break
			}
		}
		if len(candidates) >= maxCandidates {
			break
		}
	}

	// Only the two best candidates pay for the character comparison.
	bestScore := -1.0
	bestFrom, bestTo := 0, 0
	for i := 0; i < 2 && len(candidates) > 0; i++ {
		pick := 0
		for j := range candidates {
			if candidates[j].score > candidates[pick].score {
				pick = j
			}
		}
		c := candidates[pick]
		candidates = append(candidates[:pick], candidates[pick+1:]...)

		from := srcTokens[c.from].start
		to := srcTokens[c.to-1].end
		window := n.text[from:to]
		sim := similarity(needle, window)
		if sim > bestScore {
			bestScore, bestFrom, bestTo = sim, from, to
		}
	}
	if bestScore < matchCutoff {
		return Span{}, false
	}
	start, end := n.originalRange(source, bestFrom, bestTo)
	if start >= end {
		return Span{}, false
	}
	return Span{Start: start, End: end, Text: source[start:end]}, true
}

// positionalScore is the cheap first pass: how many words line up, allowing the window to be a word or two off.
func positionalScore(q, window []token) float64 {
	if len(q) == 0 || len(window) == 0 {
		return 0
	}
	matched := 0
	for i := range q {
		for _, shift := range []int{0, -1, 1} {
			j := i + shift
			if j >= 0 && j < len(window) && q[i].text == window[j].text {
				matched++
				break
			}
		}
	}
	return float64(matched) / float64(max(len(q), len(window)))
}

// similarity is one minus the edit distance over the longer length, with the distance abandoned once it cannot
// come back under the cutoff.
func similarity(a, b string) float64 {
	longest := max(len(a), len(b))
	if longest == 0 {
		return 0
	}
	limit := int(float64(longest) * (1 - matchCutoff))
	if d, ok := boundedDistance(a, b, limit); ok {
		return 1 - float64(d)/float64(longest)
	}
	return 0
}

// boundedDistance is Levenshtein distance with an early exit. It reports false when the distance is larger than
// limit, which is all the caller needs to know.
func boundedDistance(a, b string, limit int) (int, bool) {
	ar, br := []rune(a), []rune(b)
	if abs(len(ar)-len(br)) > limit {
		return 0, false
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
			rowMin = min(rowMin, curr[j])
		}
		if rowMin > limit {
			return 0, false
		}
		prev, curr = curr, prev
	}
	if prev[len(br)] > limit {
		return 0, false
	}
	return prev[len(br)], true
}

// mergeSpans sorts spans into source order and merges the ones that overlap or touch.
func mergeSpans(source string, spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	sorted := make([]Span, len(spans))
	copy(sorted, spans)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Start < sorted[j-1].Start; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	out := []Span{sorted[0]}
	for _, span := range sorted[1:] {
		last := &out[len(out)-1]
		if span.Start <= last.End {
			if span.End > last.End {
				last.End = span.End
				last.Text = source[last.Start:last.End]
			}
			continue
		}
		out = append(out, span)
	}
	return out
}

// complement is the source minus the kept spans, skipping gaps that are only whitespace.
func complement(source string, spans []Span) []Segment {
	var out []Segment
	at := 0
	for _, span := range spans {
		if span.Start > at {
			if seg, ok := segment(source, at, span.Start); ok {
				out = append(out, seg)
			}
		}
		at = max(at, span.End)
	}
	if at < len(source) {
		if seg, ok := segment(source, at, len(source)); ok {
			out = append(out, seg)
		}
	}
	return out
}

func segment(source string, start, end int) (Segment, bool) {
	if start < 0 || end > len(source) || start >= end {
		return Segment{}, false
	}
	text := source[start:end]
	if strings.TrimSpace(text) == "" {
		return Segment{}, false
	}
	return Segment{Start: start, End: end, Text: text}, true
}

func cursorOf(spans []Span) int {
	if len(spans) == 0 {
		return 0
	}
	return spans[len(spans)-1].End
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
