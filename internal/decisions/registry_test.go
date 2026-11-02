package decisions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const baseWording = `preamble: Read the message.
checks:
  intent:
    quiet: chitchat
    instructions: What is this person doing?
    options:
      chitchat: Nothing much.
      log_review: Recording a review.
  mentions_sound:
    quiet: "no"
    instructions: Does it mention sound?
    options:
      "no": Nothing about sound.
      "yes": Something about the sound.
`

const basePolicy = `backend:
  provider: openrouter
  roles:
    cairn_preread: {model: jev-1.14, timeout_ms: 2500, fallback: ""}
    cairn_fidelity: {model: jev-1.14, timeout_ms: 2500, fallback: ""}
policy:
  checks:
    intent:
      threshold: 0.65
      option_thresholds: { log_review: 0.80 }
  mentions_defaults:
    threshold: 0.75
  session_override: 0.80
`

// The shipped registry is a fixture: it is what the service loads at startup and on every reload.
func TestLoadShippedRegistry(t *testing.T) {
	r, err := Load("../../config/decisions")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.Checks) != 14 {
		t.Errorf("loaded %d checks, want the 14 in the shipped wording", len(r.Checks))
	}
	intent := r.Checks["intent"]
	if intent.Quiet != "chitchat" {
		t.Errorf("intent quiet = %q", intent.Quiet)
	}
	if intent.Threshold != 0.65 {
		t.Errorf("intent threshold = %v", intent.Threshold)
	}
	if got := intent.OptionThresholds["edit_entry"]; got != 0.80 {
		t.Errorf("edit_entry threshold = %v", got)
	}
	medium := r.Checks["medium"]
	if medium.Ambiguity == nil || medium.Ambiguity.RunnerUpMin != 0.25 || medium.Ambiguity.MaxGap != 0.30 {
		t.Errorf("medium ambiguity = %+v", medium.Ambiguity)
	}
	format := r.Checks["format"]
	if len(format.OnlyWhenIntent) != 1 || format.OnlyWhenIntent[0] != "log_review" {
		t.Errorf("format only_when_intent = %v", format.OnlyWhenIntent)
	}
	// Nine mentions_* checks take their policy from one defaults block.
	sound := r.Checks["mentions_sound"]
	if sound.Threshold != 0.75 {
		t.Errorf("mentions_sound threshold = %v, want the defaults' 0.75", sound.Threshold)
	}
	if got := r.Checks["extract_fidelity"].Phase; got != PhasePost {
		t.Errorf("extract_fidelity phase = %q", got)
	}
	if r.SessionOverride != 0.80 {
		t.Errorf("session override = %v", r.SessionOverride)
	}
	if got := r.Backend.Roles[RolePreread]; got.Model != "jev-1.14" || got.Timeout != 2500*time.Millisecond {
		t.Errorf("cairn_preread role = %+v", got)
	}
	// Every check in wording is either in the first call or the fidelity call, never both.
	if len(r.Before())+len(r.Post()) != len(r.Order) {
		t.Errorf("%d before + %d post != %d checks", len(r.Before()), len(r.Post()), len(r.Order))
	}
}

func TestRegistryRejectsBadConfig(t *testing.T) {
	cases := map[string]struct {
		wording, policy string
		want            string
	}{
		"unknown wording key": {
			replaceOnce(baseWording, "    instructions: What is this person doing?", "    colour: blue\n    instructions: What is this person doing?"),
			basePolicy, "unknown key",
		},
		"unknown policy key": {
			baseWording,
			replaceOnce(basePolicy, "      threshold: 0.65", "      threshold: 0.65\n      colour: blue"),
			"unknown key",
		},
		"no preamble": {replaceOnce(baseWording, "preamble: Read the message.", "preamble: \"\""), basePolicy, "no preamble"},
		"no intent check": {
			replaceOnce(baseWording, "  intent:", "  mood:"),
			replaceOnce(basePolicy, "    intent:", "    mood:"),
			"no intent check",
		},
		"no quiet": {replaceOnce(baseWording, "    quiet: chitchat\n", ""), basePolicy, "no quiet option"},
		"quiet not an option": {
			replaceOnce(baseWording, "    quiet: chitchat", "    quiet: mood"),
			basePolicy, "which is not one of its options",
		},
		"one option": {
			replaceOnce(baseWording, "      chitchat: Nothing much.\n      log_review: Recording a review.", "      chitchat: Nothing much."),
			basePolicy, "fewer than two options",
		},
		"option with no description": {
			replaceOnce(baseWording, "      chitchat: Nothing much.", `      chitchat: ""`),
			basePolicy, "has no description",
		},
		"no instructions": {
			replaceOnce(baseWording, "    instructions: What is this person doing?", "    instructions: \"\""),
			basePolicy, "has no instructions",
		},
		"policy names an unknown check": {
			baseWording,
			replaceOnce(basePolicy, "    intent:", "    mood:"),
			`names check "mood"`,
		},
		"check with no policy": {
			replaceOnce(baseWording, "  mentions_sound:", "  format:\n    quiet: not_stated\n    instructions: How?\n    options:\n      not_stated: Not said.\n      cinema: In a cinema.\n  mentions_sound:"),
			basePolicy, `no policy for check "format"`,
		},
		"no threshold": {
			baseWording,
			replaceOnce(basePolicy, "      threshold: 0.65\n", ""),
			"has no threshold",
		},
		"threshold zero": {
			baseWording,
			replaceOnce(basePolicy, "      threshold: 0.65", "      threshold: 0"),
			"between 0 and 1",
		},
		"threshold one": {
			baseWording,
			replaceOnce(basePolicy, "  mentions_defaults:\n    threshold: 0.75", "  mentions_defaults:\n    threshold: 1"),
			"between 0 and 1",
		},
		"threshold on the quiet option": {
			baseWording,
			replaceOnce(basePolicy, "      option_thresholds: { log_review: 0.80 }", "      option_thresholds: { chitchat: 0.80 }"),
			"quiet option",
		},
		"option threshold below the check": {
			baseWording,
			replaceOnce(basePolicy, "      option_thresholds: { log_review: 0.80 }", "      option_thresholds: { log_review: 0.50 }"),
			"below the check's own threshold",
		},
		"option threshold names an unknown option": {
			baseWording,
			replaceOnce(basePolicy, "      option_thresholds: { log_review: 0.80 }", "      option_thresholds: { log_rewiev: 0.80 }"),
			"which is not one of its options",
		},
		"only_when_intent names an unknown option": {
			baseWording,
			replaceOnce(basePolicy, "    threshold: 0.75", "    threshold: 0.75\n    only_when_intent: [log_rewiev]"),
			"which is not an intent option",
		},
		"suppress names an unknown option": {
			baseWording,
			replaceOnce(basePolicy, "    threshold: 0.75", "    threshold: 0.75\n    suppress_when_intent: [moody]"),
			"which is not an intent option",
		},
		"ambiguity out of range": {
			baseWording,
			replaceOnce(basePolicy, "      option_thresholds: { log_review: 0.80 }", "      option_thresholds: { log_review: 0.80 }\n      ambiguity: { runner_up_min: 0, max_gap: 0.30 }"),
			"runner_up_min",
		},
		"unknown phase": {
			baseWording,
			replaceOnce(basePolicy, "    threshold: 0.75", "    threshold: 0.75\n    phase: later"),
			"unknown phase",
		},
		"log_as_bug names an unknown option": {
			baseWording,
			replaceOnce(basePolicy, "    threshold: 0.75", "    threshold: 0.75\n    log_as_bug: [invented]"),
			"log_as_bug",
		},
		"unknown provider": {
			baseWording,
			replaceOnce(basePolicy, "  provider: openrouter", "  provider: local"),
			`provider "local"`,
		},
		"missing role": {
			baseWording,
			replaceOnce(basePolicy, `    cairn_fidelity: {model: jev-1.14, timeout_ms: 2500, fallback: ""}`+"\n", ""),
			`no "cairn_fidelity" role`,
		},
		"alias model": {
			baseWording,
			replaceOnce(basePolicy, "cairn_preread: {model: jev-1.14,", "cairn_preread: {model: jev-1.14-latest,"),
			"is an alias",
		},
		"tilde alias": {
			baseWording,
			replaceOnce(basePolicy, `cairn_fidelity: {model: jev-1.14,`, `cairn_fidelity: {model: "~jev-1.14",`),
			"is an alias",
		},
		"empty model": {
			baseWording,
			replaceOnce(basePolicy, `cairn_preread: {model: jev-1.14,`, `cairn_preread: {model: "",`),
			"has no model",
		},
		"timeout too short": {
			baseWording,
			replaceOnce(basePolicy, "timeout_ms: 2500, fallback: \"\"}\n    cairn_fidelity", "timeout_ms: 10, fallback: \"\"}\n    cairn_fidelity"),
			"outside",
		},
		"timeout too long": {
			baseWording,
			replaceOnce(basePolicy, `cairn_preread: {model: jev-1.14, timeout_ms: 2500`, `cairn_preread: {model: jev-1.14, timeout_ms: 60000`),
			"outside",
		},
		"bad role name": {
			baseWording,
			replaceOnce(basePolicy, "    cairn_fidelity:", "    Cairn Fidelity:"),
			"invalid role name",
		},
		"bad check name": {
			replaceOnce(baseWording, "  mentions_sound:", "  Mentions Sound:"),
			basePolicy, "invalid check name",
		},
		"bad option value": {
			replaceOnce(baseWording, "      log_review: Recording a review.", `      "Log Review": Recording a review.`),
			basePolicy, "invalid option value",
		},
		"mentions defaults with no threshold": {
			baseWording,
			replaceOnce(basePolicy, "  mentions_defaults:\n    threshold: 0.75", "  mentions_defaults: {}"),
			"mentions_defaults has no threshold",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadFiles([]byte(tc.wording), []byte(tc.policy))
			wantError(t, err, tc.want)
		})
	}
}

func TestRegistryFileFailures(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("Load on an empty directory succeeded")
	}
	if err := os.WriteFile(filepath.Join(dir, WordingFile), []byte(baseWording), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), PolicyFile) {
		t.Fatalf("Load without a policy = %v, want a missing-policy error", err)
	}
	if err := os.WriteFile(filepath.Join(dir, PolicyFile), []byte(basePolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatalf("Load with both files: %v", err)
	}
}

func TestRegistryRejectsEmptyAndHuge(t *testing.T) {
	if _, err := LoadFiles([]byte(""), []byte(basePolicy)); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty wording = %v", err)
	}
	if _, err := LoadFiles([]byte(baseWording+"---\nchecks: {}\n"), []byte(basePolicy)); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("two documents = %v", err)
	}
	huge := make([]byte, MaxFileBytes+1)
	if _, err := LoadFiles(huge, []byte(basePolicy)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("huge wording = %v", err)
	}
}

// Every check must be reachable in one of the two calls, and post_extract checks must never be sent early: a
// fidelity question asked before the body exists would be answered about nothing.
func TestRegistryPhases(t *testing.T) {
	r, err := LoadFiles([]byte(baseWording), []byte(replaceOnce(basePolicy, "    threshold: 0.75", "    threshold: 0.75\n    phase: post_extract")))
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	for _, c := range r.Before() {
		if c.Phase != PhaseBefore {
			t.Errorf("%q is in the first call with phase %q", c.Name, c.Phase)
		}
	}
	if len(r.Post()) != 1 || r.Post()[0].Name != "mentions_sound" {
		t.Errorf("post = %v", r.Post())
	}
}

func replaceOnce(s, old, new string) string {
	if !strings.Contains(s, old) {
		panic("fixture does not contain " + old)
	}
	return strings.Replace(s, old, new, 1)
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

func TestTopicForCheck(t *testing.T) {
	if topic, ok := TopicForCheck("mentions_sound"); !ok || topic != "sound" {
		t.Errorf("TopicForCheck(mentions_sound) = %q, %v", topic, ok)
	}
	for _, name := range []string{"intent", "mentions_", ""} {
		if topic, ok := TopicForCheck(name); ok {
			t.Errorf("TopicForCheck(%q) = %q, want not a mentions_ check", name, topic)
		}
	}
}

// The draft quoted every `yes` and `no` because a YAML 1.1 parser reads them as booleans. The loader's parser is
// YAML 1.2, where they stay strings, so the quoting is belt and braces rather than required. This test pins that
// down: if the parser is ever swapped for a 1.1 one, an unquoted key must fail here rather than quietly become
// `true` in a form she answers.
func TestUnquotedYesStaysAString(t *testing.T) {
	wording := replaceOnce(baseWording, `      "no": Nothing about sound.
      "yes": Something about the sound.`, `      no: Nothing about sound.
      yes: Something about the sound.`)
	r, err := LoadFiles([]byte(wording), []byte(basePolicy))
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	sound := r.Checks["mentions_sound"]
	if _, ok := sound.Options["yes"]; !ok {
		t.Fatalf("options = %v, want a yes option", sound.Options)
	}
	if _, ok := sound.Options["true"]; ok {
		t.Error("an unquoted yes became a boolean, which is the YAML 1.1 trap")
	}
}
