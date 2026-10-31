package domain

import "testing"

func TestNormaliseTitle(t *testing.T) {
	cases := map[string]string{
		"The Matrix":            "matrix",
		"  dune  ":              "dune",
		"Dune: Part Two":        "dune part two",
		"Pride & Prejudice":     "pride and prejudice",
		"The Lord of the Rings": "lord of the rings",
		"A Clockwork Orange":    "clockwork orange",
		"An Education":          "education",
		"1984":                  "1984",
		"Café Society":          "café society",
		"":                      "",
	}
	for in, want := range cases {
		if got := NormaliseTitle(in); got != want {
			t.Errorf("NormaliseTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameWorkByProviderID(t *testing.T) {
	a := WorkIdentity{Medium: "film", Title: "Dune", ProviderIDs: map[string]string{"tmdb": "438631"}}
	b := WorkIdentity{Medium: "film", Title: "Dune: Part One", ProviderIDs: map[string]string{"tmdb": "438631"}}
	if got := SameWork(a, b); got != SameWorkSame {
		t.Fatalf("SameWork = %q, want same: a provider id is stable", got)
	}
}

func TestSameWorkByExternalID(t *testing.T) {
	a := WorkIdentity{Medium: "book", Title: "Dune", ExternalIDs: map[string]string{"isbn": "9780441013593"}}
	b := WorkIdentity{Medium: "book", Title: "Dune (40th Anniversary)", ExternalIDs: map[string]string{"isbn": "9780441013593"}}
	if got := SameWork(a, b); got != SameWorkSame {
		t.Fatalf("SameWork = %q, want same: the same ISBN is the same book", got)
	}
}

func TestSameWorkByTitleYearCreator(t *testing.T) {
	a := WorkIdentity{Medium: "book", Title: "Dune", Year: 1965, Creator: "Frank Herbert"}
	b := WorkIdentity{Medium: "book", Title: "the dune", Year: 1965, Creator: "Frank Herbert."}
	if got := SameWork(a, b); got != SameWorkSame {
		t.Fatalf("SameWork = %q, want same", got)
	}
}

// A different medium is a different work, even with an identical title and year: the film is not the book.
func TestSameWorkDifferentMedium(t *testing.T) {
	a := WorkIdentity{Medium: "book", Title: "Dune", Year: 1965}
	b := WorkIdentity{Medium: "film", Title: "Dune", Year: 2021}
	if got := SameWork(a, b); got != SameWorkDifferent {
		t.Fatalf("SameWork = %q, want different", got)
	}
}

// A near miss is an ask. Two books with the same title and different years could be a work and its sequel, or two
// editions; guessing either way is worse than asking.
func TestSameWorkAsksWhenClose(t *testing.T) {
	cases := [][2]WorkIdentity{
		{
			{Medium: "book", Title: "Dune", Year: 1965, Creator: "Frank Herbert"},
			{Medium: "book", Title: "Dune", Year: 1984, Creator: "Frank Herbert"},
		},
		{
			{Medium: "film", Title: "The Thing", Year: 1982, Creator: "John Carpenter"},
			{Medium: "film", Title: "The Thing", Year: 1982, Creator: "Matthijs van Heijningen Jr."},
		},
		{
			// One character apart: almost certainly the same title typed twice, but not certain enough to merge.
			{Medium: "film", Title: "Blade Runner 2049", Year: 2017},
			{Medium: "film", Title: "Bladerunner 2049", Year: 2017},
		},
	}
	for _, pair := range cases {
		if got := SameWork(pair[0], pair[1]); got != SameWorkAsk {
			t.Errorf("SameWork(%q, %q) = %q, want ask", pair[0].Title, pair[1].Title, got)
		}
	}
}

func TestSameWorkDifferent(t *testing.T) {
	a := WorkIdentity{Medium: "film", Title: "Dune", Year: 2021}
	b := WorkIdentity{Medium: "film", Title: "Blade Runner 2049", Year: 2017}
	if got := SameWork(a, b); got != SameWorkDifferent {
		t.Fatalf("SameWork = %q, want different", got)
	}
}

// A sequel is its own work, not a near miss: "Dune Messiah" is not an edition of "Dune".
func TestSameWorkSeparatesASequel(t *testing.T) {
	a := WorkIdentity{Medium: "book", Title: "Dune Messiah", Year: 1969, Creator: "Frank Herbert"}
	b := WorkIdentity{Medium: "book", Title: "Dune", Year: 1965, Creator: "Frank Herbert"}
	if got := SameWork(a, b); got != SameWorkDifferent {
		t.Fatalf("SameWork = %q, want different", got)
	}
}

func TestTitleSimilarity(t *testing.T) {
	if got := TitleSimilarity("Dune", "dune"); got != 1 {
		t.Errorf("identical titles scored %v", got)
	}
	if got := TitleSimilarity("Dune", "Blade Runner"); got > 0.5 {
		t.Errorf("unrelated titles scored %v", got)
	}
	if got := TitleSimilarity("", "Dune"); got != 0 {
		t.Errorf("an empty title scored %v", got)
	}
}

// Bad: a missing creator or year is an absence, not a disagreement, so it must not force an ask by itself.
func TestSameWorkToleratesMissingFields(t *testing.T) {
	a := WorkIdentity{Medium: "film", Title: "Arrival"}
	b := WorkIdentity{Medium: "film", Title: "Arrival", Year: 2016, Creator: "Denis Villeneuve"}
	if got := SameWork(a, b); got != SameWorkSame {
		t.Fatalf("SameWork = %q, want same", got)
	}
}
