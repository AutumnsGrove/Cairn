package banks

import (
	"strings"
	"testing"
)

func FuzzLoadFiles(f *testing.F) {
	f.Add([]byte("categories:\n  - name: feel\n    label: Feel\n    positive: [cozy]\n"), []byte("medium: book\ninclude: [feel]\n"))
	f.Add([]byte(""), []byte(""))
	f.Add([]byte("categories: []\n"), []byte("medium: [film, tv]\ncategories:\n  - name: a\n    label: A\n    positive: [x]\n"))
	f.Add([]byte("categories:\n  - name: feel\n    label: Feel\n    positive: [cozy]\n"), []byte("medium: book\ninclude: [feel]\ninclude: [feel]\n"))
	f.Add([]byte("categories:\n  - name: warnings\n    label: W\n    kind: warnings\n    positive: [gore]\n"), []byte("medium: book\ninclude: [warnings]\n"))
	f.Fuzz(func(t *testing.T, shared, bank []byte) {
		_, _ = LoadFiles(map[string][]byte{"shared.yaml": shared, "books.yaml": bank})
	})
}

// The property the slice names: selection never offers more than three single questions, never offers a
// covered topic's question, and only ever points at a chip that exists.
func TestSelectProps(t *testing.T) {
	set, err := LoadDir("../../config/questions")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	topicPool := append([]string{""}, topics...)
	formatPool := append([]string{""}, formats...)
	genrePool := append([]string{""}, genreHints...)

	const rounds = 400
	for i := 0; i < rounds; i++ {
		req := Request{
			Medium:        set.Mediums()[i%len(set.Mediums())],
			Format:        formatPool[(i/3)%len(formatPool)],
			GenreHint:     genrePool[(i/7)%len(genrePool)],
			CoveredTopics: []string{topicPool[(i/11)%len(topicPool)]},
			Hints: map[string][]string{
				"characters": {"memorable", "definitely not a chip", ""},
				"feel":       {"COZY", "slow_burn"},
			},
		}
		sel, err := Select(set, req)
		if err != nil {
			t.Fatalf("Select(%+v): %v", req, err)
		}
		if len(sel.Questions) > MaxOfferedQuestions {
			t.Fatalf("%d questions offered, want at most %d", len(sel.Questions), MaxOfferedQuestions)
		}
		shortText := 0
		for _, q := range sel.Questions {
			if q.Topic != "" && contains(req.CoveredTopics, q.Topic) {
				t.Fatalf("offered question %q on the covered topic %q", q.ID, q.Topic)
			}
			if q.Type == TypeShortText {
				shortText++
			}
			if !q.Applies.Matches(req.Format, req.Medium, req.GenreHint) {
				t.Fatalf("offered question %q that does not apply", q.ID)
			}
		}
		if shortText > 1 {
			t.Fatalf("%d short_text questions offered, want at most one", shortText)
		}
		for _, c := range sel.Categories {
			known := make(map[string]bool)
			for _, chip := range c.Chips {
				if known[chip.Value] {
					t.Fatalf("category %q offers %q twice", c.Name, chip.Value)
				}
				known[chip.Value] = true
			}
			for _, chip := range c.Chips {
				if chip.Hinted && !known[chip.Value] {
					t.Fatalf("hint %q is not in %q", chip.Value, c.Name)
				}
			}
		}
	}
}

func TestSelectBookAudiobook(t *testing.T) {
	set := mustLoad(t)
	sel, err := Select(set, Request{
		Medium:    "book",
		Format:    "audiobook",
		GenreHint: "nonfiction",
		Hints:     map[string][]string{"characters": {"memorable", "vivid side characters"}},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}

	want := []string{"characters", "plot", "setting", "writing", "feel", "content_warnings"}
	if got := offeredNames(sel.Categories); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("categories = %v, want %v", got, want)
	}

	chars, ok := offered(sel.Categories, "characters")
	if !ok {
		t.Fatal("no characters tab")
	}
	// The mockup: hints first, dashed and never selected, then everything else alphabetically.
	if !chars.Chips[0].Hinted || !chars.Chips[1].Hinted {
		t.Fatalf("first two chips = %+v, want the hints", chars.Chips[:2])
	}
	if chars.Chips[0].Label != "memorable" || chars.Chips[1].Label != "vivid side characters" {
		t.Errorf("hints = %q, %q", chars.Chips[0].Label, chars.Chips[1].Label)
	}
	// Everything after the hints is alphabetical.
	rest := chars.Chips[2:]
	for i := 1; i < len(rest); i++ {
		if strings.ToLower(rest[i-1].Label) > strings.ToLower(rest[i].Label) {
			t.Fatalf("chips out of order: %q before %q", rest[i-1].Label, rest[i].Label)
		}
	}

	// Height of the ending (85), nonfiction (75), narration for an audiobook (70). Three, and not the
	// recommend question that ties with narration in weight but comes later in the file.
	if got := questionIDs(sel.Questions); strings.Join(got, ",") != "book_ending,book_learned,book_narration" {
		t.Errorf("questions = %v", got)
	}
}

func TestSelectCoveredTopicSkipsItsQuestion(t *testing.T) {
	set := mustLoad(t)
	sel, err := Select(set, Request{Medium: "book", CoveredTopics: []string{"ending", "characters"}})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	for _, q := range sel.Questions {
		if q.Topic == "ending" || q.Topic == "characters" {
			t.Errorf("offered %q on a covered topic", q.ID)
		}
	}
	// The covered category is never hidden; it is marked instead.
	endingCats := 0
	for _, c := range sel.Categories {
		if c.Topic == "ending" {
			endingCats++
		}
	}
	chars, _ := offered(sel.Categories, "characters")
	if !chars.Covered {
		t.Error("characters tab is not marked as covered")
	}
}

func TestSelectTVOnlyQuestions(t *testing.T) {
	set := mustLoad(t)
	sel, err := Select(set, Request{Medium: "tv", CoveredTopics: []string{"sound", "visuals", "acting", "story", "pacing"}})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	ids := questionIDs(sel.Questions)
	if !contains(ids, "tv_held_up") {
		t.Errorf("questions = %v, want tv_held_up for a tv entry", ids)
	}
	film, err := Select(set, Request{Medium: "film", CoveredTopics: []string{"sound", "visuals", "acting", "story", "pacing", "ending"}})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if contains(questionIDs(film.Questions), "tv_held_up") {
		t.Errorf("film questions = %v, want no tv-only question", questionIDs(film.Questions))
	}
}

func TestSelectHints(t *testing.T) {
	set := mustLoad(t)
	sel, err := Select(set, Request{
		Medium: "book",
		Hints: map[string][]string{
			"feel":  {"COZY", "slow_burn", "not a chip"},
			"plot":  {"slow-paced"},
			"ghost": {"anything"},
		},
	})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	feel, _ := offered(sel.Categories, "feel")
	hinted := 0
	for _, chip := range feel.Chips {
		if !chip.Hinted {
			continue
		}
		hinted++
		if chip.Value != "cozy" && chip.Value != "slow_burn" {
			t.Errorf("hinted chip = %+v", chip)
		}
	}
	if hinted != 2 {
		t.Errorf("%d hints resolved, want 2 (by label, case-insensitive, and by value)", hinted)
	}
	plot, _ := offered(sel.Categories, "plot")
	if !plot.Chips[0].Hinted || plot.Chips[0].Value != "slow_paced" {
		t.Errorf("plot first chip = %+v, want the slow_paced hint", plot.Chips[0])
	}

	want := map[string]string{"not a chip": "feel", "anything": "ghost"}
	if len(sel.Dropped) != len(want) {
		t.Fatalf("dropped = %+v, want %d", sel.Dropped, len(want))
	}
	for _, d := range sel.Dropped {
		if want[d.Hint] != d.Category {
			t.Errorf("dropped %+v", d)
		}
	}
}

func TestSelectErrors(t *testing.T) {
	set := mustLoad(t)
	if _, err := Select(set, Request{Medium: "album"}); err == nil {
		t.Error("Select(album) succeeded, want a missing-bank error")
	} else if !strings.Contains(err.Error(), "no bank") {
		t.Errorf("error = %q", err)
	}
	if _, err := Select(&Set{}, Request{Medium: "book"}); err == nil {
		t.Error("Select on an empty set succeeded")
	}
}

func offered(cats []OfferedCategory, name string) (OfferedCategory, bool) {
	for _, c := range cats {
		if c.Name == name {
			return c, true
		}
	}
	return OfferedCategory{}, false
}

func offeredNames(cats []OfferedCategory) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, c.Name)
	}
	return out
}

func questionIDs(qs []Question) []string {
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.ID)
	}
	return out
}

func mustLoad(t *testing.T) *Set {
	t.Helper()
	set, err := LoadDir("../../config/questions")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	return set
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
