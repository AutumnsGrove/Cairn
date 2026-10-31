package domain

import (
	"strings"
	"testing"
)

// Ratings are integer quarter-stars, 1 to 20 (0.25 to 5.0). The parser is the only place a written number becomes
// one, so it is tested against every shape a person actually types, plus the shapes a model might invent.

func TestParseRatingStated(t *testing.T) {
	cases := map[string]int{
		"easy 4.5":          18,
		"4.5":               18,
		"4.5/5":             18,
		"5/5":               20,
		"5 stars":           20,
		"four stars":        16,
		"4 out of 5":        16,
		"8/10":              16,
		"10/10":             20,
		"7 out of 10":       14,
		"3.75":              15,
		"0.25":              1,
		"1/4":               5,  // a quarter of five stars
		"3/4":               15, // three quarters of five
		"4 and a half":      18,
		"four and a half":   18,
		"two and a quarter": 9,
		"★★★★★":             20,
		"★★★★":              16,
		"★★★":               12,
		"4,5":               18, // a comma decimal, as much of Europe writes it
		" 4.5 ":             18,
		"rating: 4.5/5.":    18,
		"4.50":              18,
		"۵":                 20, // a Persian digit
		"４.５":               18, // a fullwidth digit
	}
	for in, want := range cases {
		got := ParseRating(in)
		if got.Status != RatingStated {
			t.Errorf("ParseRating(%q).Status = %q, want stated (got %+v)", in, got.Status, got)
		}
		if got.Quarters != want {
			t.Errorf("ParseRating(%q) = %d quarters, want %d", in, got.Quarters, want)
		}
	}
}

// Bad: a value off a quarter snaps, and says that it snapped, so the card can show what was stored.
func TestParseRatingSnaps(t *testing.T) {
	cases := map[string]int{
		"4.3": 17, // 4.25
		"4.1": 16, // 4.0
		"4.9": 20, // 5.0
		"4.6": 18, // 4.5
		"3.4": 14, // 3.5
	}
	for in, want := range cases {
		got := ParseRating(in)
		if got.Status != RatingSnapped {
			t.Errorf("ParseRating(%q).Status = %q, want snapped", in, got.Status)
		}
		if got.Quarters != want {
			t.Errorf("ParseRating(%q) = %d, want %d", in, got.Quarters, want)
		}
	}
}

// A vague phrase leaves the control empty and asks; it never guesses.
func TestParseRatingVague(t *testing.T) {
	for _, in := range []string{
		"a four and a bit",
		"four and a bit",
		"between four and five",
		"4-4.5",
		"somewhere around 4",
		"four or five stars",
		"nearly five",
		"a solid four maybe",
	} {
		got := ParseRating(in)
		if got.Status != RatingVague {
			t.Errorf("ParseRating(%q).Status = %q, want vague (got %+v)", in, got.Status, got)
		}
		if got.Quarters != 0 {
			t.Errorf("ParseRating(%q) guessed %d quarters from a vague phrase", in, got.Quarters)
		}
	}
}

func TestParseRatingNone(t *testing.T) {
	for _, in := range []string{
		"",
		"finished it",
		"loved it honestly",
		"0/10",
		"0 stars",
		"6/5",
		"11/10",
		"99",
		"2024",
		"dune",
		"four",
		"the fifth element",
	} {
		if got := ParseRating(in); got.Status != RatingNone {
			t.Errorf("ParseRating(%q) = %+v, want none", in, got)
		}
	}
}

// Bad: an enormous message is scanned in bounded time and refuses rather than truncating silently.
func TestParseRatingHugeInput(t *testing.T) {
	huge := strings.Repeat("words and more words ", 100_000) + " 4.5"
	if got := ParseRating(huge); got.Status != RatingNone {
		t.Fatalf("a 2 MB message produced %+v, want none (over the scan limit)", got)
	}
}

func TestFormatRating(t *testing.T) {
	cases := map[int]string{
		1:  "0.25",
		2:  "0.5",
		3:  "0.75",
		4:  "1",
		15: "3.75",
		18: "4.5",
		20: "5",
		0:  "",
	}
	for in, want := range cases {
		if got := FormatRating(in); got != want {
			t.Errorf("FormatRating(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestRatingValid(t *testing.T) {
	for _, q := range []int{1, 2, 10, 19, 20} {
		if !ValidRating(q) {
			t.Errorf("%d quarters should be valid", q)
		}
	}
	for _, q := range []int{-1, 0, 21, 100} {
		if ValidRating(q) {
			t.Errorf("%d quarters should be invalid", q)
		}
	}
}

// A model may hand back a float in JSON; converting it must land on the same quarter the text parser would.
func TestRatingFromFloat(t *testing.T) {
	cases := map[float64]int{
		4.5:  18,
		4.3:  17,
		0.25: 1,
		5.0:  20,
		0:    0,
		-1:   0,
		9:    0,
	}
	for in, want := range cases {
		if got := RatingFromStars(in); got != want {
			t.Errorf("RatingFromStars(%v) = %d, want %d", in, got, want)
		}
	}
}
