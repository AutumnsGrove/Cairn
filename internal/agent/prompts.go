package agent

import (
	"embed"
	"strings"
)

// The prompts are versioned files rather than Go strings, so a wording change is a diff someone can read and the
// test can check every file at once (docs/slices/S08-agent-pipeline.md 4).
//
//go:embed prompts/*.txt
var promptFS embed.FS

// SystemPrompt is the one system message the model receives.
func SystemPrompt() string { return promptText("system") }

// HintText is the template for the automatic note that precedes her latest message.
func HintText() string { return promptText("hint") }

// toolDescription reads one tool's description. An unknown name is empty, which the registry would reject, so the
// tool test asserts every registered tool has one.
func toolDescription(name string) string { return promptText("tool_" + name) }

// promptText reads and trims one embedded prompt, or returns empty for a missing file.
func promptText(name string) string {
	body, err := promptFS.ReadFile("prompts/" + name + ".txt")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// The wrap-up is nova's final request when the turn budget runs out. It is deliberately plain and tells the model
// exactly what it may do with her words.
const (
	wrapUpInstruction = "Stop looking things up. Say in one short sentence what you still need from me, or that the card is ready. Do not write any of my words."
	wrapUpFallback    = "The card is not ready yet. Try again, or fill it in by hand."
)
