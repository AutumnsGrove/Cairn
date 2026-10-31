package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	// The timezone database is embedded, because the deployed image is distroless and has no /usr/share/zoneinfo,
	// while the zone name comes from the phone on every message. Without this, "last night" would silently fall
	// back to UTC on the potato and land on the wrong day.
	_ "time/tzdata"
)

// Dates resolve against the phone, not the server: the client sends its local date and zone with every message,
// because "last night" means where the operator is, not where the potato is (PLAN.md 3).

// DateConfidence is how sure the resolver is.
type DateConfidence string

const (
	// DateClear means exactly one day matched.
	DateClear DateConfidence = "clear"
	// DateAmbiguous means more than one day could be meant, so the card must ask.
	DateAmbiguous DateConfidence = "ambiguous"
	// DateNone means the message did not say when.
	DateNone DateConfidence = "none"
)

// DateResult is a resolved day, always at local midnight in the zone it was resolved in.
type DateResult struct {
	Date       time.Time      `json:"date"`
	Confidence DateConfidence `json:"confidence"`
	Matched    string         `json:"matched,omitempty"`
}

const maxDateScan = 8000

var (
	reISODate   = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	reSlashDate = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{2,4})\b`)
	reDayMonth  = regexp.MustCompile(`\b(\d{1,2})\s+(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sep|sept|oct|nov|dec)(?:\s+(\d{4}))?\b`)
	reMonthDay  = regexp.MustCompile(`\b(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sep|sept|oct|nov|dec)\s+(\d{1,2})(?:\s*,?\s*(\d{4}))?\b`)
	reAgo       = regexp.MustCompile(`\b(\d+|a|an|one|two|three|four|five|six|seven)\s+days?\s+ago\b`)
	reOrdinal   = regexp.MustCompile(`\bthe\s+(\d{1,2})(?:st|nd|rd|th)\b`)
)

var monthNumbers = map[string]time.Month{
	"january": 1, "jan": 1, "february": 2, "feb": 2, "march": 3, "mar": 3,
	"april": 4, "apr": 4, "may": 5, "june": 6, "jun": 6, "july": 7, "jul": 7,
	"august": 8, "aug": 8, "september": 9, "sep": 9, "sept": 9,
	"october": 10, "oct": 10, "november": 11, "nov": 11, "december": 12, "dec": 12,
}

var weekdayNames = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
	"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
}

var numberWords = map[string]int{"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7}

// ResolveDate reads a day out of a sentence. It returns a clear day, an ambiguous one (so the card asks), or none.
// A nil zone means UTC.
func ResolveDate(text string, now time.Time, loc *time.Location) DateResult {
	if loc == nil {
		loc = time.UTC
	}
	if len(text) > maxDateScan {
		return DateResult{Confidence: DateNone}
	}
	s := strings.ToLower(text)
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

	// Relative days first: they are unambiguous and common.
	relative := []struct {
		phrase string
		days   int
	}{
		{"day before yesterday", -2},
		{"last night", -1},
		{"last evening", -1},
		{"yesterday", -1},
		{"tonight", 0},
		{"this evening", 0},
		{"this afternoon", 0},
		{"this morning", 0},
		{"today", 0},
	}
	for _, r := range relative {
		if strings.Contains(s, r.phrase) {
			return DateResult{Date: today.AddDate(0, 0, r.days), Confidence: DateClear, Matched: r.phrase}
		}
	}

	if m := reAgo.FindStringSubmatch(s); m != nil {
		n := 0
		if v, err := strconv.Atoi(m[1]); err == nil {
			n = v
		} else if w, ok := numberWords[m[1]]; ok {
			n = w
		}
		if n > 0 && n <= 3650 {
			return DateResult{Date: today.AddDate(0, 0, -n), Confidence: DateClear, Matched: m[0]}
		}
	}

	// An explicit ISO date is unambiguous.
	if m := reISODate.FindStringSubmatch(s); m != nil {
		if d, ok := makeDate(m[1], m[2], m[3], loc); ok {
			return DateResult{Date: d, Confidence: DateClear, Matched: m[0]}
		}
	}

	// A slashed date is ambiguous unless one of the numbers cannot be a month.
	if m := reSlashDate.FindStringSubmatch(s); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		year := expandYear(m[3])
		switch {
		case a > 12 && b <= 12:
			if d, ok := makeDate(strconv.Itoa(year), strconv.Itoa(b), strconv.Itoa(a), loc); ok {
				return DateResult{Date: d, Confidence: DateClear, Matched: m[0]}
			}
		case b > 12 && a <= 12:
			if d, ok := makeDate(strconv.Itoa(year), strconv.Itoa(a), strconv.Itoa(b), loc); ok {
				return DateResult{Date: d, Confidence: DateClear, Matched: m[0]}
			}
		case a <= 12 && b <= 12:
			return DateResult{Confidence: DateAmbiguous, Matched: m[0]}
		}
	}

	// "12 October 2025" and "October 12".
	for _, re := range []*regexp.Regexp{reDayMonth, reMonthDay} {
		if m := re.FindStringSubmatch(s); m != nil {
			day, month, year := m[1], m[2], m[3]
			if re == reMonthDay {
				day, month = m[2], m[1]
			}
			if d, ok := resolveDayMonth(day, month, year, today, loc); ok {
				return DateResult{Date: d, Confidence: DateClear, Matched: m[0]}
			}
		}
	}

	// "on friday", "last friday", "friday".
	for name, wd := range weekdayNames {
		if !containsWord(s, name) {
			continue
		}
		daysBack := (int(today.Weekday()) - int(wd) + 7) % 7
		if daysBack == 0 {
			// "Friday" on a Friday is either today or a week ago: ask.
			return DateResult{Confidence: DateAmbiguous, Matched: name}
		}
		return DateResult{Date: today.AddDate(0, 0, -daysBack), Confidence: DateClear, Matched: name}
	}

	// "the 3rd" means this month if it has passed, otherwise last month.
	if m := reOrdinal.FindStringSubmatch(s); m != nil {
		day, err := strconv.Atoi(m[1])
		if err == nil && day >= 1 && day <= 31 {
			if day <= today.Day() {
				if d, ok := makeDate(strconv.Itoa(today.Year()), strconv.Itoa(int(today.Month())), strconv.Itoa(day), loc); ok {
					return DateResult{Date: d, Confidence: DateClear, Matched: m[0]}
				}
			}
			prev := today.AddDate(0, -1, 0)
			if d, ok := makeDate(strconv.Itoa(prev.Year()), strconv.Itoa(int(prev.Month())), strconv.Itoa(day), loc); ok {
				return DateResult{Date: d, Confidence: DateClear, Matched: m[0]}
			}
		}
	}

	return DateResult{Confidence: DateNone}
}

// resolveDayMonth handles a month name with or without a year. Without one, the most recent such day is meant.
func resolveDayMonth(day, month, year string, today time.Time, loc *time.Location) (time.Time, bool) {
	m, ok := monthNumbers[month]
	if !ok {
		return time.Time{}, false
	}
	if year != "" {
		return makeDate(year, strconv.Itoa(int(m)), day, loc)
	}
	candidate, ok := makeDate(strconv.Itoa(today.Year()), strconv.Itoa(int(m)), day, loc)
	if !ok {
		return time.Time{}, false
	}
	if candidate.After(today) {
		candidate, ok = makeDate(strconv.Itoa(today.Year()-1), strconv.Itoa(int(m)), day, loc)
	}
	return candidate, ok
}

// makeDate builds local midnight and rejects a date that does not exist (31 February, month 13).
func makeDate(year, month, day string, loc *time.Location) (time.Time, bool) {
	y, err1 := strconv.Atoi(year)
	m, err2 := strconv.Atoi(month)
	d, err3 := strconv.Atoi(day)
	if err1 != nil || err2 != nil || err3 != nil {
		return time.Time{}, false
	}
	if y < 1000 || y > 9999 || m < 1 || m > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}
	built := time.Date(y, time.Month(m), d, 0, 0, 0, 0, loc)
	if built.Day() != d || int(built.Month()) != m || built.Year() != y {
		return time.Time{}, false // the date rolled over, so it does not exist
	}
	return built, true
}

// expandYear turns "25" into 2025 and leaves a four-digit year alone.
func expandYear(year string) int {
	n, err := strconv.Atoi(year)
	if err != nil {
		return 0
	}
	if n < 100 {
		return 2000 + n
	}
	return n
}

// containsWord reports whether word appears as a whole word, so "live" does not match "delivered".
func containsWord(haystack, word string) bool {
	for at := 0; at < len(haystack); {
		i := strings.Index(haystack[at:], word)
		if i < 0 {
			return false
		}
		start := at + i
		end := start + len(word)
		beforeOK := start == 0 || !isWordRune(rune(haystack[start-1]))
		afterOK := end == len(haystack) || !isWordRune(rune(haystack[end]))
		if beforeOK && afterOK {
			return true
		}
		at = start + 1
	}
	return false
}
