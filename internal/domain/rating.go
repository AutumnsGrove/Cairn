// Package domain is Cairn's pure core: ratings, dates, spans, format cues, titles, merges and the save guards.
//
// Nothing here does I/O, reads a clock or touches a global. Every function is a plain transformation of data, so
// the rules that protect the operator's own words can be tested exhaustively and fuzzed (docs/BUILD.md 4).
//
// The rule this package exists for is invariant 1: a saved body is made only of text that exists in the
// operator's own messages. The span locator in spans.go is the proof, and it is the most heavily tested code in
// the project.
package domain

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Rating is a quarter-star value as an integer, 1 to 20, plus how it was found. It is never a float: 4.75 in
// binary floating point is not always 4.75, and the stored value must compare and sort exactly
// (PLAN.md 6).
type Rating struct {
	// Quarters is the value in quarter-stars, 1 to 20. Zero means no rating.
	Quarters int
	// Status says how the value was read, so the card can show what it stored.
	Status RatingStatus
}

// RatingStatus is how a rating was found in a message.
type RatingStatus string

const (
	// RatingNone means no rating was written.
	RatingNone RatingStatus = "none"
	// RatingVague means a rating-ish phrase that cannot be pinned down, so the agent asks instead of guessing.
	RatingVague RatingStatus = "vague"
	// RatingStated means the value landed exactly on a quarter.
	RatingStated RatingStatus = "stated"
	// RatingSnapped means the value was off a quarter and was snapped to the nearest one.
	RatingSnapped RatingStatus = "snapped"
)

const (
	// MinRating is the lowest quarter-star value, 0.25.
	MinRating = 1
	// MaxRating is the highest quarter-star value, 5.0.
	MaxRating = 20
	// RatingScale is stored beside every rating so the scale can change later without a migration.
	RatingScale = "quarter_stars_20"
	// maxRatingScan bounds the text the parser looks at. Longer input is refused, never truncated silently.
	maxRatingScan = 8000
)

var (
	reVagueBit     = regexp.MustCompile(`\band an? (bit|little)\b`)
	reVagueBetween = regexp.MustCompile(`\bbetween\b.*\b(and|to)\b`)
	reVagueNear    = regexp.MustCompile(`\b(nearly|almost)\b`)
	reVagueMaybe   = regexp.MustCompile(`\b(maybe|probably|i think)\b`)
	reVagueApprox  = regexp.MustCompile(`\b(about|around|roughly|somewhere|approx\w*|like)\b`)
	reVagueEither  = regexp.MustCompile(`\b(one|two|three|four|five)\s+(or|to)\s+(one|two|three|four|five)\b`)
	reRange        = regexp.MustCompile(`\d\s*(-|–|—|to)\s*\d`)
	reNumberish    = regexp.MustCompile(`\d|\b(one|two|three|four|five)\b`)

	reFraction  = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:/|out of|of)\s*(\d+)\b`)
	reWordStar  = regexp.MustCompile(`\b(one|two|three|four|five)\s+stars?\b`)
	reWordFrac  = regexp.MustCompile(`\b(one|two|three|four|five)\s+and\s+(a half|a quarter|three quarters)\b`)
	reDigitFrac = regexp.MustCompile(`(\d+)\s+and\s+(a half|a quarter|three quarters)\b`)
	reDecimal   = regexp.MustCompile(`(\d+\.\d+)`)
	reIntUnit   = regexp.MustCompile(`(\d+)\s*stars?\b`)
	reWholeNum  = regexp.MustCompile(`^(\d+)$`)

	wordNumbers = map[string]float64{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5}
	fractions   = map[string]float64{"a half": 0.5, "a quarter": 0.25, "three quarters": 0.75}
)

// ParseRating reads a rating out of a sentence. It never guesses: a phrase it cannot pin to a quarter comes back
// as vague, and the agent asks.
func ParseRating(text string) Rating {
	if len(text) > maxRatingScan {
		// Deliberate refusal, not a silent clip: the caller should pass the snippet it means, not a whole message.
		return Rating{Status: RatingNone}
	}
	s := normalizeRatingText(text)
	if s == "" {
		return Rating{Status: RatingNone}
	}
	if isVagueRating(s) {
		return Rating{Status: RatingVague}
	}
	for _, find := range []func(string) (float64, bool){glyphStars, fractionStars, wordStars, decimalStars, unitStars, wholeNumberStars} {
		if stars, ok := find(s); ok {
			return ratingFromStars(stars)
		}
	}
	return Rating{Status: RatingNone}
}

// ratingFromStars turns a 0.25-to-5.0 value into quarters, snapping to the nearest quarter and saying so.
func ratingFromStars(stars float64) Rating {
	if stars <= 0 || stars > 5 {
		return Rating{Status: RatingNone}
	}
	exact := stars * 4
	q := int(math.Round(exact))
	if q < MinRating || q > MaxRating {
		return Rating{Status: RatingNone}
	}
	if math.Abs(exact-float64(q)) < 1e-9 {
		return Rating{Quarters: q, Status: RatingStated}
	}
	return Rating{Quarters: q, Status: RatingSnapped}
}

// isVagueRating spots the phrases that mean "somewhere around here". A vague reading must never become a stored
// number, so this is checked before any number is extracted.
func isVagueRating(s string) bool {
	if reVagueBit.MatchString(s) || reVagueBetween.MatchString(s) || reRange.MatchString(s) {
		return true
	}
	if reVagueNear.MatchString(s) {
		return true
	}
	// "maybe" and "four or five" are only vague when there is a number to be vague about.
	return reNumberish.MatchString(s) &&
		(reVagueMaybe.MatchString(s) || reVagueEither.MatchString(s) || reVagueApprox.MatchString(s))
}

// glyphStars counts filled stars: a review written with the glyphs is still a rating.
func glyphStars(s string) (float64, bool) {
	n := strings.Count(s, "★") + strings.Count(s, "⭐")
	if n == 0 {
		return 0, false
	}
	if n > 5 {
		n = 5
	}
	return float64(n), true
}

// fractionStars handles "8/10", "4 out of 5" and "3/4", which are all a fraction of five stars.
func fractionStars(s string) (float64, bool) {
	m := reFraction.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	num, _ := strconv.ParseFloat(m[1], 64)
	den, _ := strconv.ParseFloat(m[2], 64)
	if den == 0 {
		return 0, false
	}
	switch den {
	case 4, 5, 10:
	default:
		return 0, false // "7/12" is not a rating scale we know
	}
	return num / den * 5, true
}

// wordStars handles "four stars" and "four and a half".
func wordStars(s string) (float64, bool) {
	if m := reWordStar.FindStringSubmatch(s); m != nil {
		return wordNumbers[m[1]], true
	}
	if m := reWordFrac.FindStringSubmatch(s); m != nil {
		return wordNumbers[m[1]] + fractions[m[2]], true
	}
	if m := reDigitFrac.FindStringSubmatch(s); m != nil {
		whole, _ := strconv.ParseFloat(m[1], 64)
		return whole + fractions[m[2]], true
	}
	return 0, false
}

// decimalStars handles a bare decimal such as "4.5" or "0.25", which is unambiguous enough to be a rating.
func decimalStars(s string) (float64, bool) {
	m := reDecimal.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v, true
}

// unitStars handles "4 stars" and "5 out of five stars".
func unitStars(s string) (float64, bool) {
	m := reIntUnit.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v, true
}

// wholeNumberStars accepts a message that is only a number: "5", "۵". It does not accept "4 episodes", because a
// count is not a rating.
func wholeNumberStars(s string) (float64, bool) {
	m := reWholeNum.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v, true
}

// normalizeRatingText lowercases, maps every Unicode decimal digit to ASCII, and turns a comma decimal separator
// into a point. It is deliberately small: only the shapes a rating is written in survive.
func normalizeRatingText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(strings.ToLower(text))
	for i, r := range runes {
		if d, ok := digitValue(r); ok {
			// digitValue returns 0 to 9 by construction, so this indexes the ASCII digits safely.
			b.WriteByte("0123456789"[d])
			continue
		}
		// "4,5" is a decimal in much of the world; "1,000" is not something a rating uses, so this is safe.
		if r == ',' && i > 0 && i+1 < len(runes) && isASCIIDigit(runes[i-1]) && isASCIIDigit(runes[i+1]) {
			b.WriteByte('.')
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// digitValue returns the value of a Unicode decimal digit.
//
// Every decimal digit block in Unicode is ten code points long and starts at a zero, and neighbouring blocks can
// abut (the mathematical digit sets do). So the value is the offset from the start of the contiguous run, modulo
// ten. Counting back to the block start alone is wrong for those runs: it returned 10, and the fuzzer found it.
func digitValue(r rune) (int, bool) {
	if r >= '0' && r <= '9' {
		return int(r - '0'), true
	}
	if !unicode.Is(unicode.Nd, r) {
		return 0, false
	}
	runStart := r
	// The longest contiguous run of decimal digits in Unicode is about fifty code points (the mathematical
	// sets abut), so the walk back is capped well above that rather than at ten.
	for p := r - 1; p >= 0 && unicode.Is(unicode.Nd, p) && r-p <= 100; p-- {
		runStart = p
	}
	return int(r-runStart) % 10, true
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// FormatRating renders quarters as the number the card shows beside the stars: "4.5", "3.75", "5". An invalid
// value renders as the empty string rather than a wrong number.
func FormatRating(quarters int) string {
	if !ValidRating(quarters) {
		return ""
	}
	whole := quarters / 4
	rem := quarters % 4
	if rem == 0 {
		return strconv.Itoa(whole)
	}
	switch rem {
	case 1:
		return strconv.Itoa(whole) + ".25"
	case 2:
		return strconv.Itoa(whole) + ".5"
	default:
		return strconv.Itoa(whole) + ".75"
	}
}

// ValidRating reports whether a quarter-star value is in range.
func ValidRating(quarters int) bool {
	return quarters >= MinRating && quarters <= MaxRating
}

// RatingFromStars converts a value a model or a provider sent as a float into quarters. Out-of-range values
// become zero (no rating) rather than a clamped lie.
func RatingFromStars(stars float64) int {
	if math.IsNaN(stars) || stars <= 0 || stars > 5 {
		return 0
	}
	q := int(math.Round(stars * 4))
	if !ValidRating(q) {
		return 0
	}
	return q
}
