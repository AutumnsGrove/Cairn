package banks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped config is a test fixture: what the operator edits has to load, and it is the file the service
// binds into the container. A failure here means the app cannot start.
func TestLoadShippedBanks(t *testing.T) {
	set, err := LoadDir("../../config/questions")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	book, ok := set.Bank("book")
	if !ok {
		t.Fatal("no book bank")
	}
	for _, medium := range []string{"film", "tv"} {
		if _, found := set.Bank(medium); !found {
			t.Errorf("no bank for %q", medium)
		}
	}

	// The tab order is the order in PLAN.md and in the mockups: the bank's own categories, then the shared
	// ones in the order `include` lists them.
	want := []string{"characters", "plot", "setting", "writing", "feel", "content_warnings"}
	got := categoryNames(book.Categories)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("book tab order = %v, want %v", got, want)
	}

	feel, ok := book.Category("feel")
	if !ok || feel.Label != "Feel" {
		t.Fatalf("shared feel category = %+v, %v", feel, ok)
	}
	if _, found := chipValue(feel, "slow burn"); !found {
		t.Error("feel has no slow burn chip")
	}
	if _, found := chipValue(feel, "left me cold"); !found {
		t.Error("feel has no left me cold chip")
	}

	warnings, ok := book.Category("content_warnings")
	if !ok {
		t.Fatal("no content_warnings category")
	}
	if warnings.Kind != KindWarnings {
		t.Errorf("content_warnings kind = %q, want %q", warnings.Kind, KindWarnings)
	}

	// A hyphenated label is stored as its slug, so old answers survive a reword.
	plot, _ := book.Category("plot")
	if v, found := chipValue(plot, "slow-paced"); !found || v != "slow_paced" {
		t.Errorf("slow-paced value = %q, %v; want slow_paced", v, found)
	}

	narration, ok := book.Question("book_narration")
	if !ok {
		t.Fatal("no book_narration question")
	}
	if len(narration.Applies.Formats) != 1 || narration.Applies.Formats[0] != "audiobook" {
		t.Errorf("book_narration applies_when = %+v, want format audiobook", narration.Applies)
	}
}

// A bank must never offer two descriptors that store the same key, or filtering later cannot tell them apart.
func TestDuplicateChipValuePerCategory(t *testing.T) {
	_, err := LoadFiles(map[string][]byte{
		"books.yaml": []byte(`
medium: book
categories:
  - name: feel
    label: Feel
    positive: [slow burn]
    neutral: [slow burn]
`),
	})
	wantError(t, err, `duplicate chip value "slow_burn"`)
}

func TestChipRenameForm(t *testing.T) {
	set, err := LoadFiles(map[string][]byte{
		"books.yaml": []byte(`
medium: book
categories:
  - name: feel
    label: Feel
    positive:
      - { label: "new words", value: old_slug }
`),
	})
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	book, _ := set.Bank("book")
	feel, _ := book.Category("feel")
	if len(feel.Chips) != 1 {
		t.Fatalf("chips = %+v", feel.Chips)
	}
	if feel.Chips[0].Label != "new words" || feel.Chips[0].Value != "old_slug" {
		t.Errorf("chip = %+v, want the reworded label with the old value", feel.Chips[0])
	}
}

func TestStrictUnknownKeys(t *testing.T) {
	cases := map[string]struct{ file, yaml string }{
		"top level":   {"books.yaml", "medium: book\ncolour: blue\n"},
		"category":    {"books.yaml", "medium: book\ncategories:\n  - name: feel\n    label: Feel\n    colour: blue\n"},
		"chip struct": {"books.yaml", "medium: book\ncategories:\n  - name: feel\n    label: Feel\n    positive:\n      - { label: cozy, value: cozy, colour: blue }\n"},
		"question":    {"books.yaml", "medium: book\nquestions:\n  - id: q1\n    prompt: Why?\n    type: short_text\n    colour: blue\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFiles(map[string][]byte{tc.file: []byte(tc.yaml)})
			wantError(t, err, "unknown key")
		})
	}
}

func TestIncludeResolution(t *testing.T) {
	shared := `
categories:
  - name: feel
    label: Feel
    positive: [cozy]
`
	t.Run("happy", func(t *testing.T) {
		set, err := LoadFiles(map[string][]byte{
			"shared.yaml": []byte(shared),
			"books.yaml":  []byte("medium: book\ninclude: [feel]\n"),
		})
		if err != nil {
			t.Fatalf("LoadFiles: %v", err)
		}
		book, _ := set.Bank("book")
		if _, ok := book.Category("feel"); !ok {
			t.Fatal("feel was not included")
		}
		if len(set.Shared) != 1 || set.Shared[0].Name != "feel" {
			t.Errorf("shared = %+v", set.Shared)
		}
	})
	t.Run("missing", func(t *testing.T) {
		_, err := LoadFiles(map[string][]byte{"books.yaml": []byte("medium: book\ninclude: [feel]\n")})
		wantError(t, err, `includes "feel"`)
	})
	t.Run("collision", func(t *testing.T) {
		_, err := LoadFiles(map[string][]byte{
			"shared.yaml": []byte(shared),
			"books.yaml":  []byte("medium: book\ninclude: [feel]\ncategories:\n  - name: feel\n    label: Mine\n    positive: [cozy]\n"),
		})
		wantError(t, err, "duplicate category")
	})
	t.Run("wrong medium", func(t *testing.T) {
		_, err := LoadFiles(map[string][]byte{
			"shared.yaml": []byte("categories:\n  - name: warnings\n    label: Warnings\n    kind: warnings\n    media: [film]\n    positive: [gore]\n"),
			"books.yaml":  []byte("medium: book\ninclude: [warnings]\n"),
		})
		wantError(t, err, "does not apply to medium")
	})
	t.Run("duplicate include", func(t *testing.T) {
		_, err := LoadFiles(map[string][]byte{
			"shared.yaml": []byte(shared),
			"books.yaml":  []byte("medium: book\ninclude: [feel, feel]\n"),
		})
		wantError(t, err, "listed twice")
	})
}

func TestQuestionRules(t *testing.T) {
	const head = "medium: book\ncategories:\n  - name: feel\n    label: Feel\n    positive: [cozy]\n"
	cases := map[string]struct{ body, want string }{
		"two short text": {
			"questions:\n  - id: a\n    prompt: Who?\n    type: short_text\n  - id: b\n    prompt: What?\n    type: short_text\n",
			"more than one short_text",
		},
		"unknown type": {
			"questions:\n  - id: a\n    prompt: Why?\n    type: stars\n",
			`unknown type "stars"`,
		},
		"choice with no options": {
			"questions:\n  - id: a\n    prompt: Why?\n    type: choice\n",
			"has no options",
		},
		"short text with options": {
			"questions:\n  - id: a\n    prompt: Why?\n    type: short_text\n    options:\n      - { value: yes, label: Yes }\n",
			"must not have options",
		},
		"duplicate id": {
			"questions:\n  - id: a\n    prompt: One?\n    type: short_text\n  - id: a\n    prompt: Two?\n    type: choice\n    options:\n      - { value: yes, label: Yes }\n      - { value: no, label: No }\n",
			`duplicate question id "a"`,
		},
		"duplicate option value": {
			"questions:\n  - id: a\n    prompt: Why?\n    type: choice\n    options:\n      - { value: yes, label: Yes }\n      - { value: yes, label: Also yes }\n",
			`duplicate option value "yes"`,
		},
		"empty prompt": {
			"questions:\n  - id: a\n    prompt: \"  \"\n    type: short_text\n",
			"has no prompt",
		},
		"unknown topic": {
			"questions:\n  - id: a\n    prompt: Why?\n    type: short_text\n    topic: vibes\n",
			`unknown topic "vibes"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFiles(map[string][]byte{"books.yaml": []byte(head + tc.body)})
			wantError(t, err, tc.want)
		})
	}
}

func TestAppliesWhenVocabulary(t *testing.T) {
	const question = "categories:\n  - name: feel\n    label: Feel\n    positive: [cozy]\nquestions:\n  - id: a\n    prompt: Why?\n    type: short_text\n    applies_when: "
	t.Run("format list", func(t *testing.T) {
		_, err := LoadFiles(map[string][]byte{
			"books.yaml": []byte("medium: book\n" + question + "{ format: [hardback, paperback] }\n"),
		})
		if err != nil {
			t.Fatalf("LoadFiles: %v", err)
		}
	})
	t.Run("unknown key", func(t *testing.T) {
		_, err := LoadFiles(map[string][]byte{"books.yaml": []byte("medium: book\n" + question + "{ mood: cozy }\n")})
		wantError(t, err, "unknown applies_when key")
	})
	t.Run("unknown format", func(t *testing.T) {
		_, err := LoadFiles(map[string][]byte{"books.yaml": []byte("medium: book\n" + question + "{ format: vr }\n")})
		wantError(t, err, `unknown format "vr"`)
	})
	t.Run("unknown medium", func(t *testing.T) {
		_, err := LoadFiles(map[string][]byte{"books.yaml": []byte("medium: book\n" + question + "{ medium: poem }\n")})
		wantError(t, err, `unknown medium "poem"`)
	})
	t.Run("unknown genre hint", func(t *testing.T) {
		_, err := LoadFiles(map[string][]byte{"books.yaml": []byte("medium: book\n" + question + "{ genre_hint: cozy }\n")})
		wantError(t, err, `unknown genre_hint "cozy"`)
	})
}

func TestBankShape(t *testing.T) {
	cases := map[string]struct{ file, yaml, want string }{
		"no medium":        {"books.yaml", "categories: []\n", "has no medium"},
		"unknown medium":   {"books.yaml", "medium: movie\n", `unknown medium "movie"`},
		"no categories":    {"books.yaml", "medium: book\n", "no categories"},
		"duplicate name":   {"books.yaml", "medium: book\ncategories:\n  - name: feel\n    label: One\n    positive: [cozy]\n  - name: feel\n    label: Two\n    positive: [warm]\n", `duplicate category "feel"`},
		"empty label":      {"books.yaml", "medium: book\ncategories:\n  - name: feel\n    label: \" \"\n", "has no label"},
		"bad name":         {"books.yaml", "medium: book\ncategories:\n  - name: Feel!\n    label: Feel\n", `invalid category name "Feel!"`},
		"unknown kind":     {"books.yaml", "medium: book\ncategories:\n  - name: w\n    label: W\n    kind: scary\n", `unknown kind "scary"`},
		"empty chip slug":  {"books.yaml", "medium: book\ncategories:\n  - name: feel\n    label: Feel\n    positive: [\"!!!\"]\n", "no usable value"},
		"shared only":      {"shared.yaml", "categories:\n  - name: feel\n    label: Feel\n    positive: [cozy]\n", "no banks"},
		"two warnings":     {"books.yaml", "medium: book\ncategories:\n  - name: a\n    label: A\n    kind: warnings\n    positive: [gore]\n  - name: b\n    label: B\n    kind: warnings\n    positive: [war]\n", "more than one warnings"},
		"question no type": {"books.yaml", "medium: book\ncategories:\n  - name: feel\n    label: Feel\n    positive: [cozy]\nquestions:\n  - id: a\n    prompt: Why?\n", "has no type"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFiles(map[string][]byte{tc.file: []byte(tc.yaml)})
			wantError(t, err, tc.want)
		})
	}
}

func TestSizeLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("medium: book\ncategories:\n  - name: feel\n    label: Feel\n    positive: [cozy]\n#")
	for b.Len() < MaxFileBytes {
		b.WriteString(" padding")
	}
	_, err := LoadFiles(map[string][]byte{"books.yaml": []byte(b.String())})
	wantError(t, err, "larger than")
}

func TestLoadDirFailures(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		_, err := LoadDir(t.TempDir())
		wantError(t, err, "no banks")
	})
	t.Run("missing directory", func(t *testing.T) {
		_, err := LoadDir(filepath.Join(t.TempDir(), "nope"))
		wantError(t, err, "reading")
	})
	t.Run("not yaml", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "books.yaml"), []byte("medium: [unclosed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadDir(dir)
		wantError(t, err, "books.yaml")
	})
}

// The slug is the stored key, so it must be predictable, stable and ASCII: an answer recorded today must still
// match the chip after a reword.
func TestSlug(t *testing.T) {
	cases := map[string]string{
		"slow-paced":             "slow_paced",
		"bleak but worth it":     "bleak_but_worth_it",
		"  Cozy  ":               "cozy",
		"Mise-en-scène":          "mise_en_sc_ne",
		"great use of silence":   "great_use_of_silence",
		"strong language":        "strong_language",
		"8½":                     "8",
		"already_slugged":        "already_slugged",
		"!!multiple___spacing!!": "multiple_spacing",
	}
	for in, want := range cases {
		got, ok := Slug(in)
		if !ok || got != want {
			t.Errorf("Slug(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "!!!", "жанр"} {
		if got, ok := Slug(in); ok {
			t.Errorf("Slug(%q) = %q, want no usable value", in, got)
		}
	}
	for _, in := range []string{"slow_paced", "a1", "already_slugged"} {
		if !IsSlug(in) {
			t.Errorf("IsSlug(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"", "Slow", "slow-paced", "_slow", "sl ow"} {
		if IsSlug(in) {
			t.Errorf("IsSlug(%q) = true, want false", in)
		}
	}
}

func wantError(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want one mentioning %q", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error = %q, want it to mention %q", err, substr)
	}
}

func categoryNames(cats []Category) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, c.Name)
	}
	return out
}

func chipValue(c Category, label string) (string, bool) {
	for _, chip := range c.Chips {
		if chip.Label == label {
			return chip.Value, true
		}
	}
	return "", false
}
