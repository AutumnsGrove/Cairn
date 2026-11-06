package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/AutumnsGrove/nova/proto"

	"github.com/AutumnsGrove/sepia/internal/store"
)

// toolDetails is the structured part of a tool result. It is never sent to the model; the translator lifts it into
// the tool_end event so the "why" sheet can show what a call did.
type toolDetails struct {
	Inputs  map[string]any `json:"inputs,omitempty"`
	Summary string         `json:"summary,omitempty"`
	Sources []toolSource   `json:"sources,omitempty"`
	CostUSD float64        `json:"cost_usd,omitempty"`
}

// toolSource is one citation shown in the "why" sheet.
type toolSource struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// translator turns nova's events into Sepia's. It is the only place the two vocabularies meet, so a nova change
// touches this file and nothing on the frontend (docs/PROTOCOL.md 7).
type translator struct {
	ctx   context.Context
	agent *Agent
	state *RunState
	live  func(Event)

	messageID string
	open      bool
	calls     map[string]proto.ToolCall
}

// Emit handles one nova event. A write failure is logged rather than returned: nova's loop has nowhere to put it,
// and the next event will fail the same way loudly.
func (t *translator) Emit(ev proto.Event) {
	if t.calls == nil {
		t.calls = make(map[string]proto.ToolCall)
	}
	switch ev.Type {
	case proto.EventMessageStart:
		t.messageID = t.state.Agent.newID()
		t.open = true
		t.appendEvent(store.EventAssistantStart, map[string]any{"message_id": t.messageID})
	case proto.EventTextDelta:
		if !t.open {
			t.messageID = t.state.Agent.newID()
			t.open = true
			t.appendEvent(store.EventAssistantStart, map[string]any{"message_id": t.messageID})
		}
		t.appendEvent(store.EventTextDelta, map[string]any{"message_id": t.messageID, "text": ev.Text})
	case proto.EventMessageEnd:
		if t.open {
			t.appendEvent(store.EventAssistantEnd, map[string]any{"message_id": t.messageID})
			t.open = false
		}
	case proto.EventToolCall:
		if ev.Call != nil {
			t.calls[ev.Call.ID] = *ev.Call
		}
	case proto.EventToolStart:
		call := t.calls[ev.CallID]
		t.appendEvent(store.EventToolStart, map[string]any{
			"tool": call.Name, "label": toolLabel(call.Name),
		})
	case proto.EventToolUpdate:
		t.liveEvent("activity", map[string]any{"label": ev.Text})
	case proto.EventToolEnd:
		call := t.calls[ev.CallID]
		t.appendEvent(store.EventToolEnd, t.toolEnd(call, ev))
	case proto.EventUsage:
		total := t.state.PriorCost
		if ev.Total != nil {
			total += ev.Total.CostUSD
		}
		t.liveEvent("cost_update", map[string]any{"session_total_usd": total})
	case proto.EventError:
		code, text := "model_error", "The model failed."
		if ev.Error != nil {
			code, text = ev.Error.Code, ev.Error.Message
		}
		t.appendEvent(store.EventError, map[string]any{"code": code, "message": text, "retryable": false})
	case proto.EventRetry:
		t.appendEvent(store.EventNotice, map[string]any{
			"level": "info", "code": "retry", "text": "The model did not answer; trying again.",
		})
	case proto.EventCompaction:
		t.appendEvent(store.EventNotice, map[string]any{
			"level": "info", "code": "compaction", "text": "The conversation was shortened to fit.",
		})
	}
}

// toolEnd builds the one-line summary and the structured details for a finished call.
func (t *translator) toolEnd(call proto.ToolCall, ev proto.Event) map[string]any {
	data := map[string]any{"tool": call.Name, "ok": true}
	if ev.Result == nil {
		return data
	}
	data["ok"] = !ev.Result.IsError
	var details toolDetails
	if len(ev.Result.Details) > 0 {
		_ = json.Unmarshal(ev.Result.Details, &details)
	}
	if details.Inputs != nil {
		data["inputs"] = details.Inputs
	}
	summary := details.Summary
	if summary == "" {
		summary = clipSummary(ev.Result.Content)
	}
	data["summary"] = summary
	if len(details.Sources) > 0 {
		data["sources"] = details.Sources
	}
	if details.CostUSD != 0 {
		data["cost_usd"] = details.CostUSD
	}
	return data
}

// clipSummary makes a result's text fit the event's 500-rune summary field without splitting a character.
func clipSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "done"
	}
	runes := []rune(text)
	if len(runes) > 500 {
		return string(runes[:500])
	}
	return text
}

// appendEvent writes one persisted event. The sequence number is the store's, so a replay is exact.
func (t *translator) appendEvent(kind string, data any) {
	if _, err := t.state.Agent.session.Append(t.ctx, t.state.SessionID, kind, data); err != nil {
		t.state.Agent.log.Error("session event write failed", "session", t.state.SessionID, "kind", kind, "err", err.Error())
	}
}

// liveEvent publishes an event the protocol never persists. A nil sink is the test that does not care.
func (t *translator) liveEvent(kind string, data any) {
	if t.live == nil {
		return
	}
	t.live(Event{SessionID: t.state.SessionID, Kind: kind, Data: data})
}
