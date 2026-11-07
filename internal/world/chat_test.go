package world

import (
	"context"
	"net/http"
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

// A turn can report where its tokens went, and a test that asserts on a prompt asks the fake to keep the body it was
// sent. Both are off by default, because the fake must never become a second copy of a review.
func TestChatKeepsBodiesOnlyWhenAsked(t *testing.T) {
	chat := StartChat("test-chat", nil)
	defer chat.Close()
	client := openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: chat.URL()})
	request := provider.Request{
		Model:    "vendor/model",
		Messages: []message.Message{message.User("an invented sentence about a made-up book")},
	}
	stream := func() {
		if _, err := client.Stream(context.Background(), request, nil); err != nil {
			t.Fatalf("Stream: %v", err)
		}
	}
	stream()
	if kept := chat.Bodies(); len(kept) != 0 {
		t.Fatalf("the fake kept %d bodies without being asked", len(kept))
	}

	chat.KeepBodies(true)
	stream()
	bodies := chat.Bodies()
	if len(bodies) != 1 {
		t.Fatalf("kept %d bodies, want 1", len(bodies))
	}
	if !strings.Contains(string(bodies[0]), "made-up book") {
		t.Errorf("the kept body is not what was sent: %s", bodies[0])
	}
	// The copy the caller gets is its own, so a test cannot edit the fake's record by accident.
	bodies[0][0] = 'X'
	if got := chat.Bodies(); len(got) != 1 || got[0][0] == 'X' {
		t.Error("Bodies handed out its own slice")
	}

	// A request with no body at all is kept as nothing rather than panicking the fake.
	empty, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		chat.URL()+"/chat/completions", nil)
	if err != nil {
		t.Fatalf("empty request: %v", err)
	}
	resp, err := (&http.Client{}).Do(empty)
	if err != nil {
		t.Fatalf("empty request: %v", err)
	}
	_ = resp.Body.Close()
	if kept := chat.Bodies(); len(kept) != 2 || len(kept[1]) != 0 {
		t.Errorf("kept = %d bodies, last %d bytes", len(kept), len(kept[len(kept)-1]))
	}
}

// A scripted turn can name its finish reason and report a detailed usage, which the real API does and a client has to
// read. The fake writes the wire shape, so the details are asserted through nova's provider rather than its structs.
func TestChatReportsDetailedUsageAndFinish(t *testing.T) {
	chat := StartChat("test-chat", nil)
	defer chat.Close()
	chat.Script(ChatTurn{
		Text:   "done",
		Finish: "length",
		Usage: provider.Usage{
			PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25, CostUSD: 0.001,
			CachedTokens: 10, CacheWriteTokens: 2, ReasoningTokens: 3,
		},
	})
	client := openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: chat.URL()})
	resp, err := client.Stream(context.Background(), provider.Request{
		Model: "vendor/model", Messages: []message.Message{message.User("an invented sentence")},
	}, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if resp.Usage.CachedTokens != 10 || resp.Usage.ReasoningTokens != 3 || resp.Usage.CostUSD != 0.001 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.FinishReason != "length" {
		t.Errorf("finish reason = %q, want length", resp.FinishReason)
	}
}
