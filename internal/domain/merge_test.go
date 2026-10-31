package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Merging is where a wrong rule silently rewrites history, so every rule in PLAN.md 7 has a test, and the two
// properties that matter (idempotent, loses nothing) are checked over generated input.

func tmdbSource() Source {
	return Source{
		Provider:   "tmdb",
		ProviderID: "438631",
		Trust:      TrustVerified,
		Title:      "Dune",
		Year:       2021,
		Creators:   []Creator{{Role: "director", Name: "Denis Villeneuve"}},
		Details:    map[string]string{"runtime": "155", "tagline": "Beyond fear, destiny awaits."},
		CoverPath:  "covers/dune-2021.jpg",
		Raw:        json.RawMessage(`{"id":438631}`),
	}
}

func webSource() Source {
	return Source{
		Provider:  "web",
		Trust:     TrustUnverified,
		Title:     "Dune (2021 film)",
		Year:      2021,
		Creators:  []Creator{{Role: "composer", Name: "Hans Zimmer"}},
		Details:   map[string]string{"budget": "$165 million"},
		CoverPath: "covers/web-dune.jpg",
		Citations: []Citation{{URL: "https://example.test/dune", Snippet: "Dune is a 2021 film"}},
	}
}

func TestMergeTakesThePrimaryTitle(t *testing.T) {
	got := Merge("film", []Source{tmdbSource(), webSource()})
	if got.Work.Title != "Dune" {
		t.Errorf("title = %q, want the primary provider's", got.Work.Title)
	}
	if got.Work.PrimaryProvider != "tmdb" || got.Work.PrimaryID != "438631" {
		t.Errorf("primary = %s/%s", got.Work.PrimaryProvider, got.Work.PrimaryID)
	}
	if !got.Work.Verified {
		t.Error("a verified source should make the work verified")
	}
}

// A web field is proposed, never merged: it must not reach the canonical work until the operator accepts it.
func TestMergeNeverTakesWebFields(t *testing.T) {
	got := Merge("film", []Source{tmdbSource(), webSource()})
	if _, ok := got.Work.Details["budget"]; ok {
		t.Error("a web detail became canonical without a tap")
	}
	for _, c := range got.Work.Creators {
		if c.Name == "Hans Zimmer" {
			t.Error("a web creator became canonical without a tap")
		}
	}
	if len(got.Work.Sources) != 2 {
		t.Errorf("sources = %d, want both kept", len(got.Work.Sources))
	}
	if got.Work.Sources[1].Citations[0].URL == "" {
		t.Error("the web source lost its citations")
	}
}

// An accepted web field is canonical: the tap is what changes the trust, not the merge.
func TestMergeTakesAcceptedWebFields(t *testing.T) {
	web := webSource()
	web.Trust = TrustAccepted
	got := Merge("film", []Source{tmdbSource(), web})
	if got.Work.Details["budget"] != "$165 million" {
		t.Errorf("an accepted detail was dropped: %v", got.Work.Details)
	}
	found := false
	for _, c := range got.Work.Creators {
		if c.Name == "Hans Zimmer" && c.Role == "composer" {
			found = true
		}
	}
	if !found {
		t.Errorf("an accepted creator was dropped: %v", got.Work.Creators)
	}
}

func TestMergeEarliestYearWinsAndRecordsTheDisagreement(t *testing.T) {
	book := Source{Provider: "openlibrary", Trust: TrustVerified, Title: "Dune", Year: 1965}
	edition := Source{Provider: "other", Trust: TrustVerified, Title: "Dune", Year: 1984}

	got := Merge("book", []Source{edition, book})
	if got.Work.Year != 1965 {
		t.Errorf("year = %d, want the earliest", got.Work.Year)
	}
	if len(got.Conflicts) == 0 {
		t.Fatal("the disagreement was not recorded")
	}
	if got.Conflicts[0].Field != "year" {
		t.Errorf("conflict = %+v, want a year conflict", got.Conflicts[0])
	}
}

func TestMergeUnionsCreatorsWithoutDuplicates(t *testing.T) {
	a := tmdbSource()
	b := tmdbSource()
	b.Provider = "imdb"
	b.ProviderID = "tt1160419"
	b.Creators = []Creator{
		{Role: "director", Name: "denis  villeneuve"},
		{Role: "writer", Name: "Jon Spaihts"},
	}

	got := Merge("film", []Source{a, b})
	if len(got.Work.Creators) != 2 {
		t.Fatalf("creators = %+v, want two after deduplication", got.Work.Creators)
	}
	if got.Work.Creators[1].Name != "Jon Spaihts" {
		t.Errorf("creator order = %+v", got.Work.Creators)
	}
}

func TestMergeKeepsAlternateTitles(t *testing.T) {
	a := tmdbSource()
	b := Source{Provider: "imdb", Trust: TrustVerified, Title: "Dune: Part One", OriginalTitle: "Dune"}
	got := Merge("film", []Source{a, b})

	if got.Work.Title != "Dune" {
		t.Fatalf("title = %q", got.Work.Title)
	}
	if len(got.Work.AlternateTitles) != 1 || got.Work.AlternateTitles[0] != "Dune: Part One" {
		t.Errorf("alternates = %v, want the other title only", got.Work.AlternateTitles)
	}
}

func TestMergeRecordsDetailConflicts(t *testing.T) {
	a := tmdbSource()
	b := tmdbSource()
	b.Provider = "imdb"
	b.Details = map[string]string{"runtime": "156"}
	got := Merge("film", []Source{a, b})

	if got.Work.Details["runtime"] != "155" {
		t.Errorf("runtime = %q, want the primary's", got.Work.Details["runtime"])
	}
	found := false
	for _, c := range got.Conflicts {
		if c.Field == "runtime" && c.Rejected == "156" {
			found = true
		}
	}
	if !found {
		t.Errorf("conflicts = %+v, want the runtime disagreement", got.Conflicts)
	}
}

// A manual work logged during an outage is the operator's own typing, so it is canonical but not verified.
func TestMergeManualWorkIsCanonicalButUnverified(t *testing.T) {
	manual := Source{Provider: "manual", Title: "Some Zine I Read", Year: 1999}
	got := Merge("book", []Source{manual})
	if got.Work.Title != "Some Zine I Read" {
		t.Errorf("title = %q", got.Work.Title)
	}
	if got.Work.Verified {
		t.Error("a manual work must stay unverified until details are filled in")
	}
}

func TestMergeWithNothing(t *testing.T) {
	got := Merge("film", nil)
	if got.Work.Title != "" || len(got.Conflicts) != 0 {
		t.Errorf("empty merge = %+v", got)
	}
}

// A source that only carries an unverified title leaves the work unverified and its fields empty.
func TestMergeUntrustedOnly(t *testing.T) {
	got := Merge("film", []Source{webSource()})
	if got.Work.Verified {
		t.Error("a web-only merge must not be verified")
	}
	if _, ok := got.Work.Details["budget"]; ok {
		t.Error("a web detail became canonical with nothing to compare it to")
	}
	if got.Work.Title != "Dune (2021 film)" {
		t.Errorf("title = %q, want the only title there is, still unverified", got.Work.Title)
	}
}

// Property: merging is idempotent. Merging the result's own sources again changes nothing, which is what makes
// "re-merge" a safe command.
func TestMergeIsIdempotent(t *testing.T) {
	sources := []Source{tmdbSource(), webSource(), {Provider: "imdb", Trust: TrustVerified, Title: "Dune", Year: 2021}}
	first := Merge("film", sources)
	second := Merge("film", first.Work.Sources)

	if !reflect.DeepEqual(first.Work, second.Work) {
		t.Fatalf("not idempotent:\nfirst  %+v\nsecond %+v", first.Work, second.Work)
	}
	if !reflect.DeepEqual(first.Conflicts, second.Conflicts) {
		t.Fatalf("conflicts differ:\n%+v\n%+v", first.Conflicts, second.Conflicts)
	}
}

// Property: a merge never loses a field a trusted source provided. Every detail value is either canonical or in a
// recorded conflict.
func TestMergeLosesNothing(t *testing.T) {
	sources := []Source{
		tmdbSource(),
		{Provider: "imdb", Trust: TrustVerified, Title: "Dune", Year: 2021, Details: map[string]string{"runtime": "156", "budget": "165m"}},
		{Provider: "openlibrary", Trust: TrustVerified, Title: "Dune", Creators: []Creator{{Role: "author", Name: "Frank Herbert"}}},
	}
	got := Merge("film", sources)

	for _, s := range sources {
		for key, want := range s.Details {
			if got.Work.Details[key] == want {
				continue
			}
			found := false
			for _, c := range got.Conflicts {
				if c.Field == key && c.Rejected == want {
					found = true
				}
			}
			if !found {
				t.Errorf("detail %q=%q vanished", key, want)
			}
		}
		for _, c := range s.Creators {
			found := false
			for _, kept := range got.Work.Creators {
				if kept.Name == c.Name {
					found = true
				}
			}
			if !found {
				t.Errorf("creator %q vanished", c.Name)
			}
		}
	}
}
