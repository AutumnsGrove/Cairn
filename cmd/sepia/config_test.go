package main

import (
	"testing"

	"github.com/AutumnsGrove/sepia/internal/banks"
	"github.com/AutumnsGrove/sepia/internal/decisions"
)

// The shipped config is two independently edited directories that have to agree at runtime. This is the one
// place that knows both packages, so the agreement is a test rather than a reviewer's memory: a bank that names
// a topic no check fills, or a medium the decisions layer cannot return, fails here before it reaches a form.
func TestShippedConfigAgrees(t *testing.T) {
	set, err := banks.LoadDir("../../config/questions")
	if err != nil {
		t.Fatalf("banks.LoadDir: %v", err)
	}
	reg, err := decisions.Load("../../config/decisions")
	if err != nil {
		t.Fatalf("decisions.Load: %v", err)
	}

	mediums := reg.Checks["medium"]
	if mediums == nil {
		t.Fatal("the registry has no medium check")
	}

	// Every bank's medium must be an option the pre-read can actually return, or the bank is unreachable.
	for _, medium := range set.Mediums() {
		if _, ok := mediums.Options[medium]; !ok {
			t.Errorf("bank medium %q is not a medium option in the decisions registry", medium)
		}
	}

	// Every topic a bank offers must have a mentions_* check, and every mentions_* check must feed a topic a
	// bank actually offers. Either half missing means "you mentioned this" can never work.
	bankTopics := make(map[string]bool)
	for _, bank := range set.Banks {
		for _, category := range bank.Categories {
			if category.Topic != "" {
				bankTopics[category.Topic] = true
			}
		}
		for _, question := range bank.Questions {
			if question.Topic != "" {
				bankTopics[question.Topic] = true
			}
		}
	}
	for topic := range bankTopics {
		if _, ok := reg.Checks["mentions_"+topic]; !ok {
			t.Errorf("a bank uses topic %q but the decisions registry has no mentions_%s check", topic, topic)
		}
	}
	mentions := 0
	for _, check := range reg.Before() {
		topic, ok := decisions.TopicForCheck(check.Name)
		if !ok {
			continue
		}
		mentions++
		if !bankTopics[topic] {
			t.Errorf("check %q fills topic %q, which no bank offers", check.Name, topic)
		}
		if check.Quiet != "no" {
			t.Errorf("check %q is a yes/no check whose quiet option is %q", check.Name, check.Quiet)
		}
	}
	if mentions == 0 {
		t.Error("the registry has no mentions_* checks")
	}

	// The second call is the fidelity check and nothing else: the pre-read must not pay for it, and the extract
	// must exist before it is asked.
	post := reg.Post()
	if len(post) != 1 || post[0].Name != "extract_fidelity" {
		t.Errorf("post-extract checks = %v, want just extract_fidelity", post)
	}

	// A medium with a bank must be able to build a form from the shipped config alone.
	for _, medium := range set.Mediums() {
		sel, err := banks.Select(set, banks.Request{Medium: medium})
		if err != nil {
			t.Errorf("Select(%q): %v", medium, err)
			continue
		}
		if len(sel.Categories) == 0 {
			t.Errorf("Select(%q) offered no categories", medium)
		}
		if len(sel.Questions) > banks.MaxOfferedQuestions {
			t.Errorf("Select(%q) offered %d questions", medium, len(sel.Questions))
		}
		for _, category := range sel.Categories {
			if len(category.Chips) == 0 {
				t.Errorf("Select(%q) offered an empty category %q", medium, category.Name)
			}
		}
	}

	// The one option that should be impossible is configured as a bug, not as a warning.
	fidelity := reg.Checks["extract_fidelity"]
	found := false
	for _, option := range fidelity.LogAsBug {
		if option == "adds_content" {
			found = true
		}
	}
	if !found {
		t.Error("extract_fidelity does not treat adds_content as a bug")
	}
}
