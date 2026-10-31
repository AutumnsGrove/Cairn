package domain

import (
	"testing"
	"time"
)

// Dates are resolved against the phone's zone, which the client sends. These tests pin "now" and the zone so the
// result never depends on where the test runs.

var (
	london, _  = time.LoadLocation("Europe/London")
	newYork, _ = time.LoadLocation("America/New_York")
)

func nowIn(loc *time.Location) time.Time {
	// A Friday afternoon, so weekday and ordinal cases are unambiguous.
	return time.Date(2026, time.October, 30, 15, 0, 0, 0, loc)
}

func TestResolveDateRelative(t *testing.T) {
	now := nowIn(london)
	cases := map[string]int{
		"last night":             29,
		"finished it last night": 29,
		"yesterday":              29,
		"this morning":           30,
		"tonight":                30,
		"today":                  30,
		"day before yesterday":   28,
	}
	for text, day := range cases {
		got := ResolveDate(text, now, london)
		if got.Confidence != DateClear {
			t.Errorf("ResolveDate(%q).Confidence = %q, want clear", text, got.Confidence)
			continue
		}
		if got.Date.Day() != day || got.Date.Month() != time.October {
			t.Errorf("ResolveDate(%q) = %v, want October %d", text, got.Date, day)
		}
		if got.Date.Hour() != 0 || got.Date.Minute() != 0 {
			t.Errorf("ResolveDate(%q) = %v, want local midnight", text, got.Date)
		}
		if got.Date.Location() != london {
			t.Errorf("ResolveDate(%q) is in %v, want the phone's zone", text, got.Date.Location())
		}
	}
}

func TestResolveDateExplicit(t *testing.T) {
	now := nowIn(london)
	cases := map[string]string{
		"finished on 2025-10-12":     "2025-10-12",
		"read it on 12 October 2025": "2025-10-12",
		"watched October 12, 2025":   "2025-10-12",
		"saw it on 3 March 2024":     "2024-03-03",
	}
	for text, want := range cases {
		got := ResolveDate(text, now, london)
		if got.Confidence != DateClear {
			t.Errorf("ResolveDate(%q) = %q, want clear", text, got.Confidence)
			continue
		}
		if got.Date.Format("2006-01-02") != want {
			t.Errorf("ResolveDate(%q) = %s, want %s", text, got.Date.Format("2006-01-02"), want)
		}
	}
}

// A slashed date where both numbers could be a month is ambiguous: asking is cheaper than a wrong day.
func TestResolveDateAmbiguous(t *testing.T) {
	now := nowIn(london)
	for _, text := range []string{"on 10/12/2025", "finished 3/4/2025", "on friday"} {
		got := ResolveDate(text, now, london)
		if got.Confidence != DateAmbiguous {
			t.Errorf("ResolveDate(%q).Confidence = %q, want ambiguous", text, got.Confidence)
		}
		if !got.Date.IsZero() {
			t.Errorf("ResolveDate(%q) picked a day anyway: %v", text, got.Date)
		}
	}
}

// A slashed date where one number cannot be a month is clear.
func TestResolveDateUnambiguousSlash(t *testing.T) {
	now := nowIn(london)
	got := ResolveDate("on 25/12/2025", now, london)
	if got.Confidence != DateClear {
		t.Fatalf("confidence = %q", got.Confidence)
	}
	if got.Date.Format("2006-01-02") != "2025-12-25" {
		t.Errorf("date = %s, want 2025-12-25", got.Date.Format("2006-01-02"))
	}
}

func TestResolveDateWeekday(t *testing.T) {
	now := nowIn(london) // Friday 30 October
	got := ResolveDate("finished it on tuesday", now, london)
	if got.Confidence != DateClear {
		t.Fatalf("confidence = %q", got.Confidence)
	}
	if got.Date.Format("2006-01-02") != "2026-10-27" {
		t.Errorf("date = %s, want the Tuesday before", got.Date.Format("2006-01-02"))
	}
}

func TestResolveDateOrdinal(t *testing.T) {
	now := nowIn(london) // the 30th
	got := ResolveDate("finished it on the 3rd", now, london)
	if got.Confidence != DateClear {
		t.Fatalf("confidence = %q", got.Confidence)
	}
	if got.Date.Format("2006-01-02") != "2026-10-03" {
		t.Errorf("date = %s, want this month", got.Date.Format("2006-01-02"))
	}

	// A day that has not happened yet this month means last month.
	got = ResolveDate("on the 20th", time.Date(2026, time.October, 15, 12, 0, 0, 0, london), london)
	if got.Confidence != DateClear || got.Date.Format("2006-01-02") != "2026-09-20" {
		t.Errorf("date = %v (%q), want 2026-09-20", got.Date, got.Confidence)
	}

	// A day that does not exist in the month it would fall in is refused, not rolled forward.
	if got = ResolveDate("on the 31st", time.Date(2026, time.October, 15, 12, 0, 0, 0, london), london); got.Confidence == DateClear {
		t.Errorf("the 31st of a 30-day month resolved to %v", got.Date)
	}
}

func TestResolveDateDaysAgo(t *testing.T) {
	now := nowIn(london)
	for text, want := range map[string]string{
		"three days ago": "2026-10-27",
		"2 days ago":     "2026-10-28",
	} {
		got := ResolveDate(text, now, london)
		if got.Confidence != DateClear || got.Date.Format("2006-01-02") != want {
			t.Errorf("ResolveDate(%q) = %v (%q), want %s", text, got.Date, got.Confidence, want)
		}
	}
}

// Sad: no date in the message is a normal outcome, not an error.
func TestResolveDateNone(t *testing.T) {
	now := nowIn(london)
	for _, text := range []string{"", "loved it", "the ending was perfect", "a film about time"} {
		if got := ResolveDate(text, now, london); got.Confidence != DateNone {
			t.Errorf("ResolveDate(%q).Confidence = %q, want none", text, got.Confidence)
		}
	}
}

// Bad: a date that does not exist is refused rather than rolled into the next month.
func TestResolveDateImpossibleDate(t *testing.T) {
	now := nowIn(london)
	if got := ResolveDate("on 31 February 2025", now, london); got.Confidence == DateClear {
		t.Errorf("31 February resolved to %v", got.Date)
	}
}

// The phone's zone decides the day, not the server's: 00:30 UTC is still the previous evening in New York.
func TestResolveDateUsesThePhoneZone(t *testing.T) {
	instant := time.Date(2026, time.October, 30, 0, 30, 0, 0, time.UTC)
	got := ResolveDate("finished it tonight", instant, newYork)
	if got.Confidence != DateClear {
		t.Fatalf("confidence = %q", got.Confidence)
	}
	if got.Date.Format("2006-01-02") != "2026-10-29" {
		t.Errorf("date = %s, want the local day, 2026-10-29", got.Date.Format("2006-01-02"))
	}
}

// Sad: a date across a daylight-saving change still resolves to local midnight, not to a shifted hour.
func TestResolveDateAcrossDST(t *testing.T) {
	// London springs forward on 29 March 2026 at 01:00.
	now := time.Date(2026, time.April, 1, 12, 0, 0, 0, london)
	got := ResolveDate("finished it on 29 March 2026", now, london)
	if got.Confidence != DateClear {
		t.Fatalf("confidence = %q", got.Confidence)
	}
	if got.Date.Hour() != 0 || got.Date.Minute() != 0 {
		t.Errorf("date = %v, want midnight on the day the clocks changed", got.Date)
	}
	if got.Date.Format("2006-01-02") != "2026-03-29" {
		t.Errorf("date = %s", got.Date.Format("2006-01-02"))
	}
}

func TestResolveDateHugeInput(t *testing.T) {
	huge := make([]byte, maxDateScan+1)
	for i := range huge {
		huge[i] = 'a'
	}
	if got := ResolveDate(string(huge), nowIn(london), london); got.Confidence != DateNone {
		t.Errorf("an over-limit input resolved to %v", got.Date)
	}
}

func TestResolveDateNilZone(t *testing.T) {
	got := ResolveDate("yesterday", nowIn(time.UTC), nil)
	if got.Confidence != DateClear {
		t.Fatalf("confidence = %q", got.Confidence)
	}
}

// The timezone database is embedded in this package, so a zone name from the phone always resolves, even in the
// distroless image where /usr/share/zoneinfo does not exist. This test fails if the import is ever dropped.
func TestTimeZoneDatabaseIsEmbedded(t *testing.T) {
	for _, name := range []string{"Europe/London", "America/New_York", "Pacific/Auckland", "Asia/Kolkata"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("LoadLocation(%q): %v; the embedded timezone database is missing", name, err)
		}
		got := ResolveDate("last night", time.Date(2026, time.October, 30, 12, 0, 0, 0, loc), loc)
		if got.Confidence != DateClear {
			t.Errorf("%s: last night was not resolved", name)
		}
	}
}
