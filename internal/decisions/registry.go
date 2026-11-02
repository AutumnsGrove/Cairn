// Package decisions loads the decisions registry and turns a message into a pre-read.
//
// The registry is two files the operator edits by hand: wording.yaml (the checks, their prompts and options)
// and policy.yaml (what makes a check act, and which model runs it). Both are strict: an unknown key, a missing
// threshold, an option that does not exist or a model alias is a load error, because a half-understood registry
// routes a review to the wrong place silently (docs/BUILD.md 4a, docs/slices/S05-banks-and-decisions.md).
//
// The pre-read itself is nova's decide contract: one batched call per message, every check evaluated against the
// same state, odds returned and then gated by policy.go. Anything that must be true is enforced in code
// elsewhere; a probability only ever steers (PLAN.md 2).
package decisions

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The file names the service reads. They are fixed so a bind mount can replace either one alone.
const (
	WordingFile = "wording.yaml"
	PolicyFile  = "policy.yaml"
)

// The role names the app asks for. Naming them here keeps a typo in the config from silently disabling the
// pre-read: a registry without both roles fails to load.
const (
	RolePreread  = "cairn_preread"
	RoleFidelity = "cairn_fidelity"
)

// The phases a check can run in. An empty phase is the first call, before the agent turn; post_extract runs in a
// second call once code has built the body from her own spans.
const (
	PhaseBefore = ""
	PhasePost   = "post_extract"
)

// Limits from docs/STANDARDS.md 4a. They bound what a bind-mounted file can make the service do.
const (
	MaxFileBytes         = 256 << 10
	MaxChecks            = 25
	MaxInstructions      = 2_000
	MaxOptionDescription = 300
	MaxOptionsPerCheck   = 20
	MinTimeout           = 100 * time.Millisecond
	MaxTimeout           = 30 * time.Second
)

// providers is the closed set of decision backends. Cairn speaks to nova's OpenRouter client only; a second
// backend arrives behind its own adapter, not behind a new string nobody validated.
var providers = []string{"openrouter"}

// phrases are the two alias spellings nova refuses. They are rejected at load so a moved model is noticed when
// the file is edited, not when a review is logged.
var (
	aliasSuffix = "-latest"
	aliasPrefix = "~"
)

// Ambiguity is the split-read rule. When the runner-up is at least RunnerUpMin and the leader is less than
// MaxGap ahead, the pre-read asks "which one?" instead of choosing (PLAN.md 2, "decision routing").
type Ambiguity struct {
	RunnerUpMin float64 `yaml:"runner_up_min"`
	MaxGap      float64 `yaml:"max_gap"`
}

// Check is one question the decision model answers, with its policy already merged in.
type Check struct {
	Name         string
	Quiet        string
	Instructions string
	// Options maps an option value to the sentence that describes it, which is what the model receives as
	// criteria. Values are slugs, so an answer stays comparable across a reword.
	Options            map[string]string
	Threshold          float64
	OptionThresholds   map[string]float64
	OnlyWhenIntent     []string
	SuppressWhenIntent []string
	Ambiguity          *Ambiguity
	Phase              string
	LogAsBug           []string
}

// OptionNames returns the option values in alphabetical order, so anything built from a map is deterministic.
func (c Check) OptionNames() []string {
	names := make([]string, 0, len(c.Options))
	for name := range c.Options {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Role is one application role: a pinned model, its per-attempt bound and an optional fallback.
type Role struct {
	Model    string
	Fallback string
	Timeout  time.Duration
}

// Backend is what actually answers a call. Jev appears here and nowhere else in the code.
type Backend struct {
	Provider string
	Roles    map[string]Role
}

// Registry is the whole loaded config.
type Registry struct {
	Preamble        string
	Checks          map[string]*Check
	Order           []string
	SessionOverride float64
	Backend         Backend
}

// Load reads the two files from dir. A missing file is a startup error, not a quiet default.
func Load(dir string) (*Registry, error) {
	wording, err := readFile(filepath.Join(dir, WordingFile))
	if err != nil {
		return nil, err
	}
	policy, err := readFile(filepath.Join(dir, PolicyFile))
	if err != nil {
		return nil, err
	}
	return LoadFiles(wording, policy)
}

// readFile reads a bounded file, so a runaway edit cannot exhaust memory on the potato.
func readFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("decisions: reading %s: %w", path, err)
	}
	if info.Size() > MaxFileBytes {
		return nil, fmt.Errorf("decisions: %s is %d bytes, larger than the %d-byte limit", path, info.Size(), MaxFileBytes)
	}
	return os.ReadFile(path)
}

// LoadFiles parses the two documents. It is the whole loader, so a test and the fuzzer drive it without a disk.
func LoadFiles(wording, policy []byte) (*Registry, error) {
	for name, data := range map[string][]byte{WordingFile: wording, PolicyFile: policy} {
		if len(data) > MaxFileBytes {
			return nil, fmt.Errorf("decisions: %s is %d bytes, larger than the %d-byte limit", name, len(data), MaxFileBytes)
		}
	}
	var w wordingDoc
	if err := decode(wording, WordingFile, &w); err != nil {
		return nil, err
	}
	var p policyDoc
	if err := decode(policy, PolicyFile, &p); err != nil {
		return nil, err
	}
	return build(&w, &p)
}

type wordingDoc struct {
	Preamble string              `yaml:"preamble"`
	Checks   map[string]checkDoc `yaml:"checks"`
}

type checkDoc struct {
	Quiet        string            `yaml:"quiet"`
	Instructions string            `yaml:"instructions"`
	Options      map[string]string `yaml:"options"`
}

type policyDoc struct {
	Backend backendDoc  `yaml:"backend"`
	Policy  policyBlock `yaml:"policy"`
}

type backendDoc struct {
	Provider string             `yaml:"provider"`
	Roles    map[string]roleDoc `yaml:"roles"`
}

type roleDoc struct {
	Model     string `yaml:"model"`
	TimeoutMS int    `yaml:"timeout_ms"`
	Fallback  string `yaml:"fallback"`
}

type policyBlock struct {
	Checks           map[string]checkPolicyDoc `yaml:"checks"`
	MentionsDefaults *checkPolicyDoc           `yaml:"mentions_defaults"`
	SessionOverride  float64                   `yaml:"session_override"`
}

type checkPolicyDoc struct {
	Threshold          *float64           `yaml:"threshold"`
	OptionThresholds   map[string]float64 `yaml:"option_thresholds"`
	OnlyWhenIntent     []string           `yaml:"only_when_intent"`
	SuppressWhenIntent []string           `yaml:"suppress_when_intent"`
	Ambiguity          *Ambiguity         `yaml:"ambiguity"`
	Phase              string             `yaml:"phase"`
	LogAsBug           []string           `yaml:"log_as_bug"`
}

// decode parses one file strictly.
func decode(data []byte, name string, into any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("decisions: %s is empty", name)
		}
		return fmt.Errorf("decisions: %s: %w", name, friendlyYAML(err))
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decisions: %s has more than one YAML document", name)
	}
	return nil
}

// unknownFieldRE matches yaml.v3's KnownFields complaint, rewritten into the words the config comments use.
var unknownFieldRE = regexp.MustCompile(`field (\S+) not found in type \S+`)

func friendlyYAML(err error) error {
	return errors.New(unknownFieldRE.ReplaceAllString(err.Error(), `unknown key "$1"`))
}

// build validates both documents and merges policy into the checks.
func build(w *wordingDoc, p *policyDoc) (*Registry, error) {
	if len(w.Checks) == 0 {
		return nil, errors.New("decisions: wording.yaml declares no checks")
	}
	if len(w.Checks) > MaxChecks {
		return nil, fmt.Errorf("decisions: wording.yaml declares %d checks, more than the limit of %d", len(w.Checks), MaxChecks)
	}
	if strings.TrimSpace(w.Preamble) == "" {
		return nil, errors.New("decisions: wording.yaml has no preamble")
	}
	if len(w.Preamble) > MaxInstructions {
		return nil, fmt.Errorf("decisions: wording.yaml preamble is longer than %d characters", MaxInstructions)
	}

	r := &Registry{
		Preamble: strings.TrimSpace(w.Preamble),
		Checks:   make(map[string]*Check, len(w.Checks)),
		Backend:  Backend{Provider: p.Backend.Provider, Roles: map[string]Role{}},
	}
	for _, name := range sortedKeys(w.Checks) {
		c, err := buildCheck(name, w.Checks[name])
		if err != nil {
			return nil, err
		}
		r.Checks[name] = c
		r.Order = append(r.Order, name)
	}

	intent, ok := r.Checks["intent"]
	if !ok {
		return nil, errors.New("decisions: wording.yaml has no intent check, which every routing rule depends on")
	}

	if p.Policy.MentionsDefaults != nil && p.Policy.MentionsDefaults.Threshold == nil {
		return nil, fmt.Errorf("decisions: %s: mentions_defaults has no threshold", PolicyFile)
	}

	for _, name := range sortedKeys(p.Policy.Checks) {
		c, ok := r.Checks[name]
		if !ok {
			return nil, fmt.Errorf("decisions: %s names check %q, which is not in %s", PolicyFile, name, WordingFile)
		}
		if err := applyPolicy(c, p.Policy.Checks[name], intent); err != nil {
			return nil, err
		}
	}
	for _, name := range r.Order {
		c := r.Checks[name]
		if c.Threshold != 0 {
			continue
		}
		// A mentions_* check may take the shared defaults; anything else must say when it acts.
		if isMentions(name) && p.Policy.MentionsDefaults != nil {
			if err := applyPolicy(c, *p.Policy.MentionsDefaults, intent); err != nil {
				return nil, err
			}
			continue
		}
		return nil, fmt.Errorf("decisions: %s has no policy for check %q, so it could never act", PolicyFile, name)
	}

	if err := r.validatePolicy(p.Policy); err != nil {
		return nil, err
	}
	if err := r.validateBackend(p.Backend); err != nil {
		return nil, err
	}
	return r, nil
}

// buildCheck validates one check's wording.
func buildCheck(name string, doc checkDoc) (*Check, error) {
	c := &Check{
		Name:             name,
		Quiet:            doc.Quiet,
		Instructions:     strings.TrimSpace(doc.Instructions),
		Options:          make(map[string]string, len(doc.Options)),
		OptionThresholds: map[string]float64{},
	}
	if !isSlug(name) {
		return nil, fmt.Errorf("decisions: %s: invalid check name %q; use lower-case words and underscores", WordingFile, name)
	}
	if c.Instructions == "" {
		return nil, fmt.Errorf("decisions: %s: check %q has no instructions", WordingFile, name)
	}
	if len(c.Instructions) > MaxInstructions {
		return nil, fmt.Errorf("decisions: %s: check %q has instructions longer than %d characters", WordingFile, name, MaxInstructions)
	}
	if len(doc.Options) < 2 {
		return nil, fmt.Errorf("decisions: %s: check %q has fewer than two options", WordingFile, name)
	}
	if len(doc.Options) > MaxOptionsPerCheck {
		return nil, fmt.Errorf("decisions: %s: check %q has more than %d options", WordingFile, name, MaxOptionsPerCheck)
	}
	for value, description := range doc.Options {
		if !isSlug(value) {
			return nil, fmt.Errorf("decisions: %s: check %q has an invalid option value %q", WordingFile, name, value)
		}
		description = strings.TrimSpace(description)
		if description == "" {
			return nil, fmt.Errorf("decisions: %s: check %q: option %q has no description", WordingFile, name, value)
		}
		if len(description) > MaxOptionDescription {
			return nil, fmt.Errorf("decisions: %s: check %q: option %q describes itself in more than %d characters", WordingFile, name, value, MaxOptionDescription)
		}
		c.Options[value] = description
	}
	if c.Quiet == "" {
		return nil, fmt.Errorf("decisions: %s: check %q has no quiet option; nothing would be the default", WordingFile, name)
	}
	if _, ok := c.Options[c.Quiet]; !ok {
		return nil, fmt.Errorf("decisions: %s: check %q names %q as its quiet option, which is not one of its options", WordingFile, name, c.Quiet)
	}
	return c, nil
}

// applyPolicy merges one policy block into a check and validates the parts that depend on the wording.
func applyPolicy(c *Check, doc checkPolicyDoc, intent *Check) error {
	where := fmt.Sprintf("check %q", c.Name)
	if doc.Threshold == nil {
		return fmt.Errorf("decisions: %s: %s has no threshold", PolicyFile, where)
	}
	if err := checkRate(*doc.Threshold); err != nil {
		return fmt.Errorf("decisions: %s: %s: threshold %v %w", PolicyFile, where, *doc.Threshold, err)
	}
	c.Threshold = *doc.Threshold

	for option, rate := range doc.OptionThresholds {
		if _, ok := c.Options[option]; !ok {
			return fmt.Errorf("decisions: %s: %s: option_thresholds names %q, which is not one of its options", PolicyFile, where, option)
		}
		if option == c.Quiet {
			return fmt.Errorf("decisions: %s: %s gives its quiet option %q a threshold; a quiet option never acts", PolicyFile, where, option)
		}
		if err := checkRate(rate); err != nil {
			return fmt.Errorf("decisions: %s: %s: option %q threshold %v %w", PolicyFile, where, option, rate, err)
		}
		if rate < *doc.Threshold {
			return fmt.Errorf("decisions: %s: %s: option %q needs %v, which is below the check's own threshold %v", PolicyFile, where, option, rate, *doc.Threshold)
		}
		c.OptionThresholds[option] = rate
	}

	intentOptions := make(map[string]bool, len(intent.Options))
	for option := range intent.Options {
		intentOptions[option] = true
	}
	for _, option := range doc.OnlyWhenIntent {
		if !intentOptions[option] {
			return fmt.Errorf("decisions: %s: %s: only_when_intent names %q, which is not an intent option", PolicyFile, where, option)
		}
	}
	for _, option := range doc.SuppressWhenIntent {
		if !intentOptions[option] {
			return fmt.Errorf("decisions: %s: %s: suppress_when_intent names %q, which is not an intent option", PolicyFile, where, option)
		}
	}
	c.OnlyWhenIntent = doc.OnlyWhenIntent
	c.SuppressWhenIntent = doc.SuppressWhenIntent

	if doc.Ambiguity != nil {
		if err := checkRate(doc.Ambiguity.RunnerUpMin); err != nil {
			return fmt.Errorf("decisions: %s: %s: ambiguity runner_up_min %v %w", PolicyFile, where, doc.Ambiguity.RunnerUpMin, err)
		}
		if err := checkRate(doc.Ambiguity.MaxGap); err != nil {
			return fmt.Errorf("decisions: %s: %s: ambiguity max_gap %v %w", PolicyFile, where, doc.Ambiguity.MaxGap, err)
		}
		c.Ambiguity = doc.Ambiguity
	}

	switch doc.Phase {
	case PhaseBefore, PhasePost:
		c.Phase = doc.Phase
	default:
		return fmt.Errorf("decisions: %s: %s has unknown phase %q; known phases are %q and %q", PolicyFile, where, doc.Phase, PhaseBefore, PhasePost)
	}

	for _, option := range doc.LogAsBug {
		if _, ok := c.Options[option]; !ok {
			return fmt.Errorf("decisions: %s: %s: log_as_bug names %q, which is not one of its options", PolicyFile, where, option)
		}
	}
	c.LogAsBug = doc.LogAsBug
	return nil
}

// checkRate keeps every probability setting inside (0, 1): a threshold of 0 fires on nothing said and 1 can
// never fire, so both are a config mistake, not a setting.
func checkRate(rate float64) error {
	if rate <= 0 || rate >= 1 {
		return errors.New("must be between 0 and 1")
	}
	return nil
}

// validatePolicy checks the parts of policy that are not per check.
func (r *Registry) validatePolicy(p policyBlock) error {
	if err := checkRate(p.SessionOverride); err != nil {
		return fmt.Errorf("decisions: %s: session_override %v %w", PolicyFile, p.SessionOverride, err)
	}
	r.SessionOverride = p.SessionOverride
	if p.MentionsDefaults != nil && p.MentionsDefaults.Threshold == nil {
		return fmt.Errorf("decisions: %s: mentions_defaults has no threshold", PolicyFile)
	}
	return nil
}

// validateBackend checks the provider and every role, including the alias rule nova applies at call time.
func (r *Registry) validateBackend(b backendDoc) error {
	if !slices.Contains(providers, b.Provider) {
		return fmt.Errorf("decisions: %s: backend provider %q is not one of %s", PolicyFile, b.Provider, strings.Join(providers, ", "))
	}
	for name, doc := range b.Roles {
		if !isSlug(name) {
			return fmt.Errorf("decisions: %s: invalid role name %q", PolicyFile, name)
		}
		model, err := pinnedModel(name, "model", doc.Model)
		if err != nil {
			return err
		}
		fallback := ""
		if strings.TrimSpace(doc.Fallback) != "" {
			fallback, err = pinnedModel(name, "fallback", doc.Fallback)
			if err != nil {
				return err
			}
		}
		timeout := time.Duration(doc.TimeoutMS) * time.Millisecond
		if timeout < MinTimeout || timeout > MaxTimeout {
			return fmt.Errorf("decisions: %s: role %q timeout_ms %d is outside %d to %d", PolicyFile, name, doc.TimeoutMS, MinTimeout.Milliseconds(), MaxTimeout.Milliseconds())
		}
		r.Backend.Roles[name] = Role{Model: model, Fallback: fallback, Timeout: timeout}
	}
	for _, name := range []string{RolePreread, RoleFidelity} {
		if _, ok := b.Roles[name]; !ok {
			return fmt.Errorf("decisions: %s: backend has no %q role", PolicyFile, name)
		}
	}
	r.Backend.Provider = b.Provider
	return nil
}

// pinnedModel refuses an empty id and both alias spellings, so a model that moves without notice is a load
// error here rather than a surprise in a review's odds.
func pinnedModel(role, field, model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("decisions: %s: role %q has no %s", PolicyFile, role, field)
	}
	if strings.HasPrefix(model, aliasPrefix) || strings.HasSuffix(model, aliasSuffix) {
		return "", fmt.Errorf("decisions: %s: role %q %s %q is an alias; pin a version-level id", PolicyFile, role, field, model)
	}
	return model, nil
}

// isMentions reports whether a check feeds the banks' covered topics.
func isMentions(name string) bool {
	return strings.HasPrefix(name, "mentions_")
}

// TopicForCheck maps a mentions_* check to the bank topic it fills. It checks the prefix only: the topic
// vocabulary belongs to the banks, and the shipped config's agreement with it is asserted where both packages
// are known (cmd/cairn/config_test.go).
func TopicForCheck(name string) (string, bool) {
	topic, ok := strings.CutPrefix(name, "mentions_")
	if !ok || topic == "" {
		return "", false
	}
	return topic, true
}

// isSlug is the stored form a value, option or role name must already be in.
func isSlug(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// sortedKeys returns a map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Before is the checks one pre-read call sends: everything whose phase is not post_extract.
func (r *Registry) Before() []*Check {
	var out []*Check
	for _, name := range r.Order {
		if r.Checks[name].Phase == PhaseBefore {
			out = append(out, r.Checks[name])
		}
	}
	return out
}

// Post returns the checks that run in the fidelity call after the body is built.
func (r *Registry) Post() []*Check {
	var out []*Check
	for _, name := range r.Order {
		if r.Checks[name].Phase == PhasePost {
			out = append(out, r.Checks[name])
		}
	}
	return out
}
