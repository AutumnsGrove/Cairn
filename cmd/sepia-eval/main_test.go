package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/AutumnsGrove/sepia/internal/agent"
	"github.com/AutumnsGrove/sepia/internal/config"
)

// The live eval spends money and is never run by CI. The test proves the guard, not the spend.
func TestRunWithoutAKeyRefuses(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, []string{"PATH=/usr/bin"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "SEPIA_OPENROUTER_API_KEY") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "sepia-eval") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// A bad corpus path is a normal command error, not a panic.
func TestRunRefusesAMissingCorpus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"testdata/does-not-exist.json"}, []string{"SEPIA_OPENROUTER_API_KEY=test"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

// The pinned model is named in two places by necessity: config cannot import the agent, and the agent must not
// depend on config. The composition root holds the two together.
func TestPinnedModelMatchesTheConfigDefault(t *testing.T) {
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Model != agent.PinnedModel {
		t.Fatalf("config default %q, agent pin %q", cfg.Model, agent.PinnedModel)
	}
}
