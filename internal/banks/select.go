package banks

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// MaxOfferedQuestions is the limit from docs/STANDARDS.md: at most three single questions per form. The loader
// may hold more; a bank grows a question without breaking anything, and selection picks the heaviest three.
const MaxOfferedQuestions = 3

// Request is what selection needs to know about one entry: the medium, the two fields the questions may depend
// on, the topics the pre-read says the message already covered, and any chips the model pointed at.
type Request struct {
	Medium        string
	Format        string
	GenreHint     string
	CoveredTopics []string
	// Hints maps a category name to the chip labels or values the model suggested for it.
	Hints map[string][]string
}

// OfferedChip is one chip in an offered category. Hinted chips are drawn dashed and are never pre-selected.
type OfferedChip struct {
	Value  string
	Label  string
	Tone   string
	Hinted bool
}

// OfferedCategory is one tab. A covered category is marked "you mentioned this", never hidden.
type OfferedCategory struct {
	Name    string
	Label   string
	Kind    string
	Topic   string
	Covered bool
	Chips   []OfferedChip
}

// DroppedHint is a suggestion that was thrown away, kept so it can be logged rather than silently ignored.
type DroppedHint struct {
	Category string
	Hint     string
	Reason   string
}

// Selection is one entry's form: the tabs to show and the questions to ask.
type Selection struct {
	Medium     string
	Categories []OfferedCategory
	Questions  []Question
	Dropped    []DroppedHint
}

// Select turns a bank plus what the pre-read found into the form to offer. It is pure: the same request always
// yields the same selection, which is what makes a reopened draft show the same chips.
func Select(set *Set, req Request) (Selection, error) {
	if set == nil || len(set.Banks) == 0 {
		return Selection{}, errors.New("banks: no banks loaded")
	}
	bank, ok := set.Bank(req.Medium)
	if !ok {
		return Selection{}, fmt.Errorf("banks: no bank for medium %q", req.Medium)
	}

	covered := make(map[string]bool, len(req.CoveredTopics))
	for _, topic := range req.CoveredTopics {
		covered[topic] = true
	}

	sel := Selection{Medium: req.Medium}
	offeredNames := make(map[string]bool, len(bank.Categories))
	for _, category := range bank.Categories {
		if !category.Applies.Matches(req.Format, req.Medium, req.GenreHint) {
			continue
		}
		offeredNames[category.Name] = true
		sel.Categories = append(sel.Categories, OfferedCategory{
			Name:    category.Name,
			Label:   category.Label,
			Kind:    category.Kind,
			Topic:   category.Topic,
			Covered: category.Topic != "" && covered[category.Topic],
			Chips:   orderChips(category.Chips, hintedValues(category, req.Hints[category.Name], &sel)),
		})
	}

	// A hint naming a category the bank does not have is dropped too, or a typo in a prompt would be invisible.
	for _, name := range sortedKeys(req.Hints) {
		if len(req.Hints[name]) == 0 {
			continue
		}
		if _, ok := bank.Category(name); ok && offeredNames[name] {
			continue
		}
		reason := "category not offered"
		if _, ok := bank.Category(name); !ok {
			reason = "no such category"
		}
		for _, hint := range req.Hints[name] {
			sel.Dropped = append(sel.Dropped, DroppedHint{Category: name, Hint: hint, Reason: reason})
		}
	}

	sel.Questions = chooseQuestions(bank, req, covered)
	sortDropped(sel.Dropped)
	return sel, nil
}

// hintedValues resolves one category's hints to chip values. A label matches case-insensitively; a value matches
// exactly. Anything else is recorded as dropped, because the closed vocabulary is the point.
func hintedValues(category Category, hints []string, sel *Selection) map[string]bool {
	if len(hints) == 0 {
		return nil
	}
	out := make(map[string]bool, len(hints))
	seen := make(map[string]bool, len(hints))
	for _, hint := range hints {
		trimmed := strings.TrimSpace(hint)
		if trimmed == "" {
			sel.Dropped = append(sel.Dropped, DroppedHint{Category: category.Name, Hint: hint, Reason: "empty hint"})
			continue
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		value, ok := matchChip(category.Chips, trimmed)
		if !ok {
			sel.Dropped = append(sel.Dropped, DroppedHint{Category: category.Name, Hint: trimmed, Reason: "not in the vocabulary"})
			continue
		}
		out[value] = true
	}
	return out
}

// matchChip finds a chip by value or by label, ignoring case and surrounding space on the label.
func matchChip(chips []Chip, hint string) (string, bool) {
	for _, chip := range chips {
		if chip.Value == strings.ToLower(hint) {
			return chip.Value, true
		}
	}
	for _, chip := range chips {
		if strings.EqualFold(strings.TrimSpace(chip.Label), hint) {
			return chip.Value, true
		}
	}
	return "", false
}

// orderChips puts the hinted chips first and everything else after, each group alphabetical by label. The mockup
// draws the hints dashed at the top of the tab, then the whole vocabulary in the order she reads it.
func orderChips(chips []Chip, hinted map[string]bool) []OfferedChip {
	out := make([]OfferedChip, 0, len(chips))
	for _, chip := range chips {
		out = append(out, OfferedChip{Value: chip.Value, Label: chip.Label, Tone: chip.Tone, Hinted: hinted[chip.Value]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Hinted != out[j].Hinted {
			return out[i].Hinted
		}
		li, lj := strings.ToLower(out[i].Label), strings.ToLower(out[j].Label)
		if li != lj {
			return li < lj
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// chooseQuestions picks the heaviest applicable questions: a covered topic is skipped, a question whose
// applies_when fails is skipped, ties keep file order, and at most one short_text is offered.
func chooseQuestions(bank *Bank, req Request, covered map[string]bool) []Question {
	candidates := make([]Question, 0, len(bank.Questions))
	for _, q := range bank.Questions {
		if q.Topic != "" && covered[q.Topic] {
			continue
		}
		if !q.Applies.Matches(req.Format, req.Medium, req.GenreHint) {
			continue
		}
		candidates = append(candidates, q)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Weight > candidates[j].Weight })

	out := make([]Question, 0, MaxOfferedQuestions)
	shortText := 0
	for _, q := range candidates {
		if len(out) == MaxOfferedQuestions {
			break
		}
		if q.Type == TypeShortText {
			if shortText > 0 {
				continue
			}
			shortText++
		}
		out = append(out, q)
	}
	return out
}

// sortedKeys returns a map's keys in order, so a selection built from a map is still deterministic.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// sortDropped orders the dropped hints so two runs on one message produce identical logs.
func sortDropped(dropped []DroppedHint) {
	sort.SliceStable(dropped, func(i, j int) bool {
		if dropped[i].Category != dropped[j].Category {
			return dropped[i].Category < dropped[j].Category
		}
		return dropped[i].Hint < dropped[j].Hint
	})
}
