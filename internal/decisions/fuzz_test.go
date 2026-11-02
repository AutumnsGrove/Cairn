package decisions

import (
	"os"
	"testing"
)

// FuzzLoadRegistry proves a hand-edited registry cannot panic the service or reach a use: both outcomes are a
// registry or an error, and the error names the file (docs/TESTING.md 4).
func FuzzLoadRegistry(f *testing.F) {
	f.Add([]byte(baseWording), []byte(basePolicy))
	f.Add([]byte(""), []byte(""))
	f.Add([]byte("checks: {}\n"), []byte("policy: {}\n"))
	f.Add([]byte(baseWording), []byte("backend:\n  provider: openrouter\n  roles: {}\npolicy:\n  checks: {}\n"))
	f.Add([]byte("preamble: x\nchecks:\n  intent: {quiet: a, instructions: b, options: {a: x, b: y}}\n"), []byte(basePolicy))

	// The shipped pair is the most important seed: whatever else changes, the real config must stay parseable.
	if wording, err := os.ReadFile("../../config/decisions/" + WordingFile); err == nil {
		if policy, err := os.ReadFile("../../config/decisions/" + PolicyFile); err == nil {
			f.Add(wording, policy)
		}
	}

	f.Fuzz(func(t *testing.T, wording, policy []byte) {
		_, _ = LoadFiles(wording, policy)
	})
}
