package world

import (
	"context"
	"strings"
	"testing"

	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"
	"github.com/AutumnsGrove/nova/provider/openrouter"
)

// The fake speaks the real wire, so a test of the fake is a test of nova's provider against it. That is the seam
// S08 relies on: the agent loop is real, only the model's replies are scripted.
func TestChatSpeaksTheOpenRouterStream(t *testing.T) {
	chat := StartChat("test-chat", nil)
	defer chat.Close()
	chat.Script(
		ChatTurn{Text: "Looking that up.", ToolCalls: []ChatToolCall{{
			ID: "call_1", Name: "media_lookup", Arguments: `{"title":"Dune"}`,
		}}},
		ChatTurn{Text: "Found it."},
	)

	client := openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: chat.URL()})
	var kinds []string
	resp, err := client.Stream(context.Background(), provider.Request{
		Model:    "vendor/model",
		Messages: []message.Message{message.User("I finished Dune.")},
	}, func(ev provider.StreamEvent) { kinds = append(kinds, string(ev.Type)) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Message.Content != "Looking that up." {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Function.Name != "media_lookup" {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	if resp.Message.ToolCalls[0].Function.Arguments != `{"title":"Dune"}` {
		t.Errorf("arguments = %q", resp.Message.ToolCalls[0].Function.Arguments)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CostUSD != 0.0001 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if len(kinds) == 0 {
		t.Error("the stream produced no events")
	}

	if _, err := client.Stream(context.Background(), provider.Request{
		Model: "vendor/model", Messages: []message.Message{message.User("and?")},
	}, nil); err != nil {
		t.Fatalf("second Stream: %v", err)
	}
	if got := chat.Turns(); got != 2 {
		t.Errorf("served %d turns, want 2", got)
	}

	// A fault is sticky: the second and third calls see it too.
	chat.SetFault("/v1/chat/completions", FaultError)
	for i := 0; i < 2; i++ {
		_, err := client.Stream(context.Background(), provider.Request{
			Model: "vendor/model", Messages: []message.Message{message.User("hi")},
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("call %d under a sticky fault = %v", i+1, err)
		}
	}
}
