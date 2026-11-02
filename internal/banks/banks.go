// Package banks loads the question banks and selects what to offer for one entry.
//
// A bank is YAML the operator edits by hand and the service hot-reloads, so it is treated as untrusted input:
// unknown keys, duplicate values, an unknown vocabulary word and an oversized file are all load errors, and a
// bank that fails to load never reaches a form. The loader is pure; reading files and watching them lives in
// store.go (docs/slices/S05-banks-and-decisions.md 1 and 6).
package banks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kinds of category. A warnings category is factual, not opinion, and is shown on the entry as one quiet line.
const (
	KindChips    = "chips"
	KindWarnings = "warnings"
)

// Question types. A short_text question is the one the model may not answer with a chip.
const (
	TypeChoice    = "choice"
	TypeShortText = "short_text"
)

// MaxFileBytes bounds one bank file. The shipped banks are about 6 KiB each; the limit exists so a runaway
// edit cannot exhaust memory on the potato (docs/STANDARDS.md 4a).
const MaxFileBytes = 256 << 10

// The other limits from the same table.
const (
	MaxCategories         = 12
	MaxChipsPerCategory   = 80
	MaxQuestions          = 25
	MaxPromptChars        = 200
	MaxOptionLabelChars   = 60
	MaxOptionsPerQuestion = 20
)

// topics is the closed set a category or question may name. It matches the decisions registry's
// `mentions_*` checks, which is what makes "you already said this" possible.
var topics = []string{"sound", "visuals", "acting", "story", "pacing", "ending", "characters", "world", "writing"}

// formats is the domain's vocabulary, not the decisions check's. Only the entry's own fields are trusted when
// choosing a question (PLAN.md 2; S05 decision 7).
var formats = []string{"cinema", "streaming", "broadcast", "audiobook", "hardback", "paperback", "ebook", "physical", "live"}

// mediums is the closed set of media Cairn knows. S17 adds a vocabulary per new medium, not a new spelling.
var mediums = []string{"book", "film", "tv", "game", "album", "podcast", "theatre", "other"}

// genreHints are the coarse kinds a provider's genres are reduced to. A bank question may depend on one.
var genreHints = []string{"fiction", "nonfiction", "documentary"}

// Chip is one selectable descriptor. Value is the stable stored key; Label is what she reads, and may be
// reworded without touching old answers.
type Chip struct {
	Value string
	Label string
	Tone  string
}

// Category is one tab of the Describe it step.
type Category struct {
	Name    string
	Label   string
	Kind    string
	Topic   string
	Media   []string
	Applies Applies
	Chips   []Chip
}

// Option is one answer in a single-choice question.
type Option struct {
	Value string
	Label string
}

// Question is one of the at most three single questions offered for an entry.
type Question struct {
	ID      string
	Topic   string
	Weight  int
	Prompt  string
	Type    string
	Applies Applies
	Options []Option
}

// Applies is the `applies_when` condition on a category or question. An empty Applies always applies; two or
// more values in any field mean "any of these".
type Applies struct {
	Formats    []string
	Mediums    []string
	GenreHints []string
}

// Bank is one medium's question set: its own categories followed by the shared ones it includes, in the order
// the tabs are shown.
type Bank struct {
	Path       string
	Mediums    []string
	Categories []Category
	Questions  []Question
}

// Set is every bank plus the shared vocabularies they draw from.
type Set struct {
	Shared []Category
	Banks  []*Bank
}

// Bank returns the bank serving medium.
func (s *Set) Bank(medium string) (*Bank, bool) {
	for _, bank := range s.Banks {
		if slices.Contains(bank.Mediums, medium) {
			return bank, true
		}
	}
	return nil, false
}

// Mediums returns every medium that has a bank, in file order.
func (s *Set) Mediums() []string {
	var out []string
	for _, bank := range s.Banks {
		out = append(out, bank.Mediums...)
	}
	return out
}

// Category returns a category by name.
func (b *Bank) Category(name string) (Category, bool) {
	for _, c := range b.Categories {
		if c.Name == name {
			return c, true
		}
	}
	return Category{}, false
}

// Question returns a question by id.
func (b *Bank) Question(id string) (Question, bool) {
	for _, q := range b.Questions {
		if q.ID == id {
			return q, true
		}
	}
	return Question{}, false
}

// LoadDir reads every *.yaml file in dir, where shared.yaml holds the vocabularies and every other file is a
// bank. A directory with no bank is an error: an empty question set is a misconfiguration, not a state.
func LoadDir(dir string) (*Set, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("banks: reading %s: %w", dir, err)
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("banks: reading %s: %w", path, err)
		}
		if info.Size() > MaxFileBytes {
			return nil, fmt.Errorf("banks: %s is %d bytes, larger than the %d-byte limit", path, info.Size(), MaxFileBytes)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("banks: reading %s: %w", path, err)
		}
		files[entry.Name()] = data
	}
	return LoadFiles(files)
}

// LoadFiles parses a bank set from file contents keyed by base name. It is the whole loader, so a test and the
// fuzzer can drive it without touching the disk.
func LoadFiles(files map[string][]byte) (*Set, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)

	set := &Set{}
	sharedByName := make(map[string]Category)

	// The vocabularies are read first: a bank may include them whatever the two file names sort like.
	var bankNames []string
	for _, name := range names {
		if name != sharedName {
			bankNames = append(bankNames, name)
			continue
		}
		data := files[name]
		if len(data) > MaxFileBytes {
			return nil, fmt.Errorf("banks: %s is %d bytes, larger than the %d-byte limit", name, len(data), MaxFileBytes)
		}
		doc, err := decodeDoc(name, data)
		if err != nil {
			return nil, err
		}
		if len(doc.Medium) > 0 {
			return nil, fmt.Errorf("banks: %s: %s must not declare a medium; it is a vocabulary", name, sharedName)
		}
		if len(doc.Questions) > 0 {
			return nil, fmt.Errorf("banks: %s: %s must not declare questions; a bank does that", name, sharedName)
		}
		if len(doc.Include) > 0 {
			return nil, fmt.Errorf("banks: %s: %s must not include another file", name, sharedName)
		}
		cats, err := buildCategories(name, doc.Categories)
		if err != nil {
			return nil, err
		}
		set.Shared = append(set.Shared, cats...)
		for _, c := range cats {
			sharedByName[c.Name] = c
		}
	}

	for _, name := range bankNames {
		data := files[name]
		if len(data) > MaxFileBytes {
			return nil, fmt.Errorf("banks: %s is %d bytes, larger than the %d-byte limit", name, len(data), MaxFileBytes)
		}
		bank, err := loadBank(name, data, sharedByName)
		if err != nil {
			return nil, err
		}
		set.Banks = append(set.Banks, bank)
	}
	if len(set.Banks) == 0 {
		return nil, errors.New("banks: no banks found; a directory needs at least one file besides " + sharedName)
	}
	return set, nil
}

const sharedName = "shared.yaml"

// doc is one YAML file. Every field is a struct so KnownFields(true) can reject anything else.
type doc struct {
	Medium     mediumList    `yaml:"medium"`
	Include    []string      `yaml:"include"`
	Categories []categoryDoc `yaml:"categories"`
	Questions  []questionDoc `yaml:"questions"`
}

type categoryDoc struct {
	Name     string   `yaml:"name"`
	Label    string   `yaml:"label"`
	Kind     string   `yaml:"kind"`
	Topic    string   `yaml:"topic"`
	Media    []string `yaml:"media"`
	Applies  Applies  `yaml:"applies_when"`
	Positive []Chip   `yaml:"positive"`
	Negative []Chip   `yaml:"negative"`
	Mixed    []Chip   `yaml:"mixed"`
	Neutral  []Chip   `yaml:"neutral"`
}

type questionDoc struct {
	ID      string   `yaml:"id"`
	Topic   string   `yaml:"topic"`
	Weight  int      `yaml:"weight"`
	Prompt  string   `yaml:"prompt"`
	Type    string   `yaml:"type"`
	Applies Applies  `yaml:"applies_when"`
	Options []Option `yaml:"options"`
}

// decodeDoc parses one file strictly, with a message a person can act on.
func decodeDoc(name string, data []byte) (doc, error) {
	var d doc
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		if errors.Is(err, io.EOF) {
			return doc{}, fmt.Errorf("banks: %s is empty", name)
		}
		return doc{}, fmt.Errorf("banks: %s: %w", name, friendlyYAML(err))
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return doc{}, fmt.Errorf("banks: %s has more than one YAML document", name)
	}
	return d, nil
}

// unknownFieldRE matches yaml.v3's KnownFields complaint, which is accurate but not written for a person.
var unknownFieldRE = regexp.MustCompile(`field (\S+) not found in type \S+`)

// friendlyYAML rewrites a strict-decoding error into the words the config's own comments use.
func friendlyYAML(err error) error {
	msg := unknownFieldRE.ReplaceAllString(err.Error(), `unknown key "$1"`)
	return errors.New(msg)
}

// loadBank builds one bank: its own categories, then the shared ones it includes, then its questions.
func loadBank(name string, data []byte, shared map[string]Category) (*Bank, error) {
	d, err := decodeDoc(name, data)
	if err != nil {
		return nil, err
	}
	if len(d.Medium) == 0 {
		return nil, fmt.Errorf("banks: %s has no medium", name)
	}
	seenMedium := make(map[string]bool, len(d.Medium))
	for _, medium := range d.Medium {
		if !slices.Contains(mediums, medium) {
			return nil, fmt.Errorf("banks: %s: unknown medium %q", name, medium)
		}
		if seenMedium[medium] {
			return nil, fmt.Errorf("banks: %s lists medium %q twice", name, medium)
		}
		seenMedium[medium] = true
	}

	bank := &Bank{Path: name, Mediums: d.Medium}
	cats, err := buildCategories(name, d.Categories)
	if err != nil {
		return nil, err
	}
	bank.Categories = append(bank.Categories, cats...)

	seenInclude := make(map[string]bool, len(d.Include))
	for _, inc := range d.Include {
		if seenInclude[inc] {
			return nil, fmt.Errorf("banks: %s: include %q is listed twice", name, inc)
		}
		seenInclude[inc] = true
		sharedCat, ok := shared[inc]
		if !ok {
			return nil, fmt.Errorf("banks: %s includes %q, which is not in %s", name, inc, sharedName)
		}
		if len(sharedCat.Media) > 0 {
			for _, medium := range d.Medium {
				if !slices.Contains(sharedCat.Media, medium) {
					return nil, fmt.Errorf("banks: %s: shared category %q does not apply to medium %q", name, inc, medium)
				}
			}
		}
		bank.Categories = append(bank.Categories, sharedCat)
	}
	if len(bank.Categories) == 0 {
		return nil, fmt.Errorf("banks: %s has no categories and includes none", name)
	}
	if err = checkCategoryNames(name, bank.Categories); err != nil {
		return nil, err
	}

	questions, err := buildQuestions(name, d.Questions)
	if err != nil {
		return nil, err
	}
	bank.Questions = questions
	return bank, nil
}

// buildCategories validates the categories in one file and expands the tone groups into one chip list.
func buildCategories(name string, docs []categoryDoc) ([]Category, error) {
	if len(docs) > MaxCategories {
		return nil, fmt.Errorf("banks: %s has %d categories, more than the limit of %d", name, len(docs), MaxCategories)
	}
	seen := make(map[string]bool, len(docs))
	warnings := 0
	out := make([]Category, 0, len(docs))
	for _, cd := range docs {
		c := Category{
			Name:    cd.Name,
			Label:   strings.TrimSpace(cd.Label),
			Kind:    cd.Kind,
			Topic:   cd.Topic,
			Media:   cd.Media,
			Applies: cd.Applies,
		}
		if !IsSlug(c.Name) {
			return nil, fmt.Errorf("banks: %s: invalid category name %q; use lower-case words and underscores", name, c.Name)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("banks: %s: duplicate category %q", name, c.Name)
		}
		seen[c.Name] = true
		if c.Label == "" {
			return nil, fmt.Errorf("banks: %s: category %q has no label", name, c.Name)
		}
		switch c.Kind {
		case "":
			c.Kind = KindChips
		case KindChips:
		case KindWarnings:
			warnings++
			if c.Topic != "" {
				return nil, fmt.Errorf("banks: %s: warnings category %q must not have a topic", name, c.Name)
			}
		default:
			return nil, fmt.Errorf("banks: %s: category %q has unknown kind %q", name, c.Name, c.Kind)
		}
		if c.Topic != "" && !slices.Contains(topics, c.Topic) {
			return nil, fmt.Errorf("banks: %s: category %q has unknown topic %q", name, c.Name, c.Topic)
		}
		for _, m := range c.Media {
			if !slices.Contains(mediums, m) {
				return nil, fmt.Errorf("banks: %s: category %q has unknown medium %q", name, c.Name, m)
			}
		}

		groups := []struct {
			tone  string
			chips []Chip
		}{
			{"positive", cd.Positive},
			{"negative", cd.Negative},
			{"mixed", cd.Mixed},
			{"neutral", cd.Neutral},
		}
		values := make(map[string]bool)
		labels := make(map[string]bool)
		for _, group := range groups {
			for _, chip := range group.chips {
				label := strings.TrimSpace(chip.Label)
				if label == "" {
					return nil, fmt.Errorf("banks: %s: category %q has a chip with no label", name, c.Name)
				}
				if len(label) > MaxOptionLabelChars {
					return nil, fmt.Errorf("banks: %s: category %q has the label %q, longer than %d characters", name, c.Name, label, MaxOptionLabelChars)
				}
				value := chip.Value
				if value == "" {
					slugged, ok := Slug(label)
					if !ok {
						return nil, fmt.Errorf("banks: %s: category %q has a chip %q with no usable value", name, c.Name, label)
					}
					value = slugged
				} else if !IsSlug(value) {
					return nil, fmt.Errorf("banks: %s: category %q has an invalid chip value %q", name, c.Name, value)
				}
				if values[value] {
					return nil, fmt.Errorf("banks: %s: category %q has duplicate chip value %q", name, c.Name, value)
				}
				values[value] = true
				if labels[strings.ToLower(label)] {
					return nil, fmt.Errorf("banks: %s: category %q has duplicate chip label %q", name, c.Name, label)
				}
				labels[strings.ToLower(label)] = true
				c.Chips = append(c.Chips, Chip{Value: value, Label: label, Tone: group.tone})
			}
		}
		if len(c.Chips) == 0 {
			return nil, fmt.Errorf("banks: %s: category %q has no chips", name, c.Name)
		}
		if len(c.Chips) > MaxChipsPerCategory {
			return nil, fmt.Errorf("banks: %s: category %q has %d chips, more than the limit of %d", name, c.Name, len(c.Chips), MaxChipsPerCategory)
		}
		out = append(out, c)
	}
	if warnings > 1 {
		return nil, fmt.Errorf("banks: %s has more than one warnings category", name)
	}
	return out, nil
}

// buildQuestions validates the single questions of one bank.
func buildQuestions(name string, docs []questionDoc) ([]Question, error) {
	if len(docs) > MaxQuestions {
		return nil, fmt.Errorf("banks: %s has %d questions, more than the limit of %d", name, len(docs), MaxQuestions)
	}
	seenID := make(map[string]bool, len(docs))
	shortText := 0
	out := make([]Question, 0, len(docs))
	for _, qd := range docs {
		q := Question{
			ID:      qd.ID,
			Topic:   qd.Topic,
			Weight:  qd.Weight,
			Prompt:  strings.TrimSpace(qd.Prompt),
			Type:    qd.Type,
			Applies: qd.Applies,
			Options: qd.Options,
		}
		if q.ID == "" {
			return nil, fmt.Errorf("banks: %s has a question with no id", name)
		}
		if seenID[q.ID] {
			return nil, fmt.Errorf("banks: %s: duplicate question id %q", name, q.ID)
		}
		seenID[q.ID] = true
		if q.Prompt == "" {
			return nil, fmt.Errorf("banks: %s: question %q has no prompt", name, q.ID)
		}
		if len(q.Prompt) > MaxPromptChars {
			return nil, fmt.Errorf("banks: %s: question %q has a prompt longer than %d characters", name, q.ID, MaxPromptChars)
		}
		if q.Weight < 0 || q.Weight > 100 {
			return nil, fmt.Errorf("banks: %s: question %q has weight %d, outside 0 to 100", name, q.ID, q.Weight)
		}
		if q.Topic != "" && !slices.Contains(topics, q.Topic) {
			return nil, fmt.Errorf("banks: %s: question %q has unknown topic %q", name, q.ID, q.Topic)
		}
		switch q.Type {
		case "":
			return nil, fmt.Errorf("banks: %s: question %q has no type", name, q.ID)
		case TypeShortText:
			shortText++
			if len(q.Options) > 0 {
				return nil, fmt.Errorf("banks: %s: question %q is short_text and must not have options", name, q.ID)
			}
			if shortText > 1 {
				return nil, fmt.Errorf("banks: %s has more than one short_text question", name)
			}
		case TypeChoice:
			if len(q.Options) < 2 {
				return nil, fmt.Errorf("banks: %s: question %q has no options; a choice needs at least two", name, q.ID)
			}
			if len(q.Options) > MaxOptionsPerQuestion {
				return nil, fmt.Errorf("banks: %s: question %q has more than %d options", name, q.ID, MaxOptionsPerQuestion)
			}
			values := make(map[string]bool, len(q.Options))
			for i, opt := range q.Options {
				label := strings.TrimSpace(opt.Label)
				if label == "" {
					return nil, fmt.Errorf("banks: %s: question %q has an option with no label", name, q.ID)
				}
				if len(label) > MaxOptionLabelChars {
					return nil, fmt.Errorf("banks: %s: question %q has the option %q, longer than %d characters", name, q.ID, label, MaxOptionLabelChars)
				}
				value := opt.Value
				if value == "" {
					slugged, ok := Slug(label)
					if !ok {
						return nil, fmt.Errorf("banks: %s: question %q has an option %q with no usable value", name, q.ID, label)
					}
					value = slugged
				} else if !IsSlug(value) {
					return nil, fmt.Errorf("banks: %s: question %q has an invalid option value %q", name, q.ID, value)
				}
				if values[value] {
					return nil, fmt.Errorf("banks: %s: question %q has duplicate option value %q", name, q.ID, value)
				}
				values[value] = true
				q.Options[i] = Option{Value: value, Label: label}
			}
		default:
			return nil, fmt.Errorf("banks: %s: question %q has unknown type %q", name, q.ID, q.Type)
		}
		out = append(out, q)
	}
	return out, nil
}

// checkCategoryNames rejects a category name that appears twice in one bank, which is what an `include` of a
// category the bank also defines produces.
func checkCategoryNames(name string, cats []Category) error {
	seen := make(map[string]bool, len(cats))
	for _, c := range cats {
		if seen[c.Name] {
			return fmt.Errorf("banks: %s: duplicate category %q; a bank may not define a shared category it includes", name, c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

// mediumList accepts `medium: book` and `medium: [film, tv]`, so one bank can serve a pair.
type mediumList []string

// UnmarshalYAML reads a scalar or a sequence.
func (m *mediumList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value == "" || node.Tag == "!!null" {
			*m = nil
			return nil
		}
		*m = []string{node.Value}
		return nil
	case yaml.SequenceNode:
		var out []string
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return errors.New("medium must be a name or a list of names")
			}
			out = append(out, item.Value)
		}
		*m = out
		return nil
	default:
		return errors.New("medium must be a name or a list of names")
	}
}

// UnmarshalYAML reads either a plain label or the {label, value} rename form.
func (c *Chip) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		c.Label = node.Value
		return nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i].Value, node.Content[i+1].Value
			switch key {
			case "label":
				c.Label = value
			case "value":
				c.Value = value
			default:
				return fmt.Errorf("unknown key %q in a chip; a chip is a label or {label, value}", key)
			}
		}
		return nil
	default:
		return errors.New("a chip must be a string or {label, value}")
	}
}

// UnmarshalYAML reads an option the same way a chip is written.
func (o *Option) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		o.Label = node.Value
		return nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i].Value, node.Content[i+1].Value
			switch key {
			case "label":
				o.Label = value
			case "value":
				o.Value = value
			default:
				return fmt.Errorf("unknown key %q in an option; an option is a label or {value, label}", key)
			}
		}
		return nil
	default:
		return errors.New("an option must be a string or {value, label}")
	}
}

// UnmarshalYAML reads the applies_when condition, whose keys are a closed set.
func (a *Applies) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return errors.New("applies_when must be a map")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		values, err := scalarList(node.Content[i+1])
		if err != nil {
			return fmt.Errorf("applies_when %s: %w", key, err)
		}
		switch key {
		case "format":
			if err := checkVocabulary("format", values, formats); err != nil {
				return err
			}
			a.Formats = values
		case "medium":
			if err := checkVocabulary("medium", values, mediums); err != nil {
				return err
			}
			a.Mediums = values
		case "genre_hint":
			if err := checkVocabulary("genre_hint", values, genreHints); err != nil {
				return err
			}
			a.GenreHints = values
		default:
			return fmt.Errorf("unknown applies_when key %q; known keys are format, medium, genre_hint", key)
		}
	}
	return nil
}

// scalarList reads a scalar or a sequence of scalars.
func scalarList(node *yaml.Node) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		return []string{node.Value}, nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, errors.New("expected a name or a list of names")
			}
			out = append(out, item.Value)
		}
		return out, nil
	default:
		return nil, errors.New("expected a name or a list of names")
	}
}

// checkVocabulary rejects a word outside a closed set, naming the set and the word.
func checkVocabulary(kind string, values, known []string) error {
	for _, v := range values {
		if !slices.Contains(known, v) {
			return fmt.Errorf("unknown %s %q; known values are %s", kind, v, strings.Join(known, ", "))
		}
	}
	return nil
}

// Matches reports whether a category or question applies to this entry. An empty Applies always matches.
func (a Applies) Matches(format, medium, genreHint string) bool {
	return matchesOne(a.Formats, format) && matchesOne(a.Mediums, medium) && matchesOne(a.GenreHints, genreHint)
}

// matchesOne is true when the field is unconstrained or this value is one of the listed ones.
func matchesOne(allowed []string, value string) bool {
	if len(allowed) == 0 {
		return true
	}
	return value != "" && slices.Contains(allowed, value)
}
