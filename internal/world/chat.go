package world

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AutumnsGrove/nova/provider"
)

// Chat is the fake OpenRouter chat endpoint. It speaks the same server-sent-event stream the real API does, so an
// agent test drives nova's real openaicompat provider and the real agent loop; only the model is scripted.
//
// A scripted turn is one reply. A turn with tool calls makes the loop execute them and come back for the next
// turn, which is how a whole review flow is replayed in order. When the script runs out the fake answers with a
// short final line, so a test that only cares about the first turn stays readable.
type Chat struct {
	*Service

	mu      sync.Mutex
	turns   []ChatTurn
	next    int
	bodies  [][]byte
	keepRaw bool
}

// ChatToolCall is one tool call in a scripted turn. Arguments is the raw JSON string the model would send, so a
// test can script malformed arguments on purpose.
type ChatToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ChatTurn is one scripted reply.
type ChatTurn struct {
	Text      string
	ToolCalls []ChatToolCall
	// Finish is "stop" or "tool_calls"; an empty value is inferred from whether the turn has tool calls.
	Finish string
	// Usage is reported with the turn. Zero means a small, cheap default, so a test that does not care about
	// tokens still sees a cost row.
	Usage provider.Usage
}

// NewChat builds the fake with a fixed clock when the caller gives none.
func NewChat(name string, clock Clock) *Chat {
	return &Chat{Service: NewService(name, clock)}
}

// Script replaces the scripted turns.
func (c *Chat) Script(turns ...ChatTurn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turns = append([]ChatTurn(nil), turns...)
	c.next = 0
}

// Turns returns how many scripted turns have been served.
func (c *Chat) Turns() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.next
}

// KeepBodies asks the fake to keep the request bodies. It is off by default, because a recorder must never become a
// copy of what she wrote; a test that asserts what the model saw turns it on with synthetic fixtures only.
func (c *Chat) KeepBodies(keep bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keepRaw = keep
}

// Bodies returns the kept request bodies, oldest first.
func (c *Chat) Bodies() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.bodies))
	for i, body := range c.bodies {
		out[i] = append([]byte(nil), body...)
	}
	return out
}

// ServeHTTP answers one chat-completions request.
func (c *Chat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fault := c.Fault(r.URL.Path)
	c.record(r)
	if c.keepRaw {
		body := readBounded(r, 64<<10)
		c.mu.Lock()
		c.bodies = append(c.bodies, body)
		c.mu.Unlock()
	}

	switch fault {
	case FaultSlow:
		select {
		case <-time.After(slowDelay):
		case <-r.Context().Done():
			return
		}
	case FaultError:
		writeJSONError(w, statusError, c.Name()+" is unavailable")
		return
	case FaultRateLimit:
		if c.RetryAfter() > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(c.RetryAfter().Seconds())))
		}
		writeJSONError(w, http.StatusTooManyRequests, c.Name()+" asks you to slow down")
		return
	case FaultNotFound:
		writeJSONError(w, http.StatusNotFound, "no such model")
		return
	case FaultDrop:
		dropConnection(w)
		return
	case FaultMalformed:
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"choices\": not json\n\n")
		return
	case FaultStall:
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
		return
	case FaultHuge:
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: ")
		_, _ = w.Write(make([]byte, hugeBody))
		_, _ = fmt.Fprint(w, "\n\n")
		return
	case FaultNone:
	}

	turn := c.take()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	write := func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format, args...)
		if flusher != nil {
			flusher.Flush()
		}
	}

	model := c.Name()
	chunk := func(delta map[string]any, finish string, usage *provider.Usage) {
		payload := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		if usage != nil {
			// The wire spells the reported cost "cost", not provider.Usage's JSON name, so the fake writes the
			// wire shape rather than reusing the struct's tags.
			wireUsage := map[string]any{
				"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens,
				"total_tokens": usage.TotalTokens, "cost": usage.CostUSD,
			}
			if usage.CachedTokens != 0 || usage.CacheWriteTokens != 0 {
				wireUsage["prompt_tokens_details"] = map[string]any{
					"cached_tokens": usage.CachedTokens, "cache_write_tokens": usage.CacheWriteTokens,
				}
			}
			if usage.ReasoningTokens != 0 {
				wireUsage["completion_tokens_details"] = map[string]any{"reasoning_tokens": usage.ReasoningTokens}
			}
			payload["choices"] = []any{}
			payload["usage"] = wireUsage
		}
		body, _ := json.Marshal(payload)
		write("data: %s\n\n", body)
	}

	if turn.Text != "" {
		chunk(map[string]any{"role": "assistant", "content": turn.Text}, "", nil)
	}
	for i, call := range turn.ToolCalls {
		chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": i, "id": call.ID, "type": "function",
			"function": map[string]any{"name": call.Name, "arguments": call.Arguments},
		}}}, "", nil)
	}
	finish := turn.Finish
	if finish == "" {
		if len(turn.ToolCalls) > 0 {
			finish = "tool_calls"
		} else {
			finish = "stop"
		}
	}
	chunk(map[string]any{}, finish, nil)

	usage := turn.Usage
	if usage == (provider.Usage{}) {
		usage = provider.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, CostUSD: 0.0001, CostSource: "provider"}
	}
	chunk(map[string]any{}, "", &usage)
	write("data: [DONE]\n\n")
}

// take returns the next scripted turn, or a clean final line when the script is exhausted.
func (c *Chat) take() ChatTurn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.next >= len(c.turns) {
		return ChatTurn{Text: "Done."}
	}
	turn := c.turns[c.next]
	c.next++
	return turn
}

// readBounded reads at most limit bytes, so a hostile test body cannot make the fake allocate without bound.
func readBounded(r *http.Request, limit int64) []byte {
	if r.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, limit))
	return body
}

// writeJSONError writes the OpenRouter-shaped error object.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"code":%d}}`, message, status)
}

// ChatServer is the chat fake on a real loopback listener.
type ChatServer struct {
	*Chat
	http *httptest.Server
}

// StartChat mounts the fake on a loopback listener.
func StartChat(name string, clock Clock) *ChatServer {
	chat := NewChat(name, clock)
	ts := httptest.NewServer(chat)
	return &ChatServer{Chat: chat, http: ts}
}

// URL is the base URL, with the /v1 path the OpenRouter provider appends chat/completions to.
func (s *ChatServer) URL() string { return strings.TrimRight(s.http.URL, "/") + "/v1" }

// Close stops the listener.
func (s *ChatServer) Close() { s.http.Close() }
