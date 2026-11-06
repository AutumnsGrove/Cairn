package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"

	novaagent "github.com/AutumnsGrove/nova/agent"
	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"

	"github.com/AutumnsGrove/sepia/internal/decisions"
	"github.com/AutumnsGrove/sepia/internal/domain"
	"github.com/AutumnsGrove/sepia/internal/providers"
	"github.com/AutumnsGrove/sepia/internal/session"
)

// errBudget ends a run that has spent its token budget. It is a defined outcome with a notice, not a crash.
var errBudget = errors.New("agent: turn budget reached")

// maxToolArgs bounds one tool call's arguments before they are decoded. A model that sends a novel is refused
// rather than parsed (docs/STANDARDS.md 4a).
const maxToolArgs = 8 << 10

// RunState is the dependency bundle on tool.Context.Data. Nova never imports it; every tool type-asserts it back.
// It is one turn's memory: the parsed fields, the pre-read, the candidates, the card and the ledger counters.
type RunState struct {
	Agent     *Agent
	SessionID string
	Text      string
	// SourceText is every message she typed, joined. A body is proved against this before it is stored.
	SourceText string
	Parsed     Parsed
	PreRead    decisions.Outcome
	Medium     string

	Draft      *session.Draft
	Work       *providers.Result
	Candidates map[string]providers.Candidate
	Chosen     *providers.Candidate
	Revises    string

	AskedDescribe bool
	Answers       []domain.Answer
	skipAll       bool
	SaveCalled    bool
	// SaveAttempted is set when save_entry is called at all, even if it is refused.
	SaveAttempted bool
	// StartedCard is set when this turn put a card on a work, so a card left from an earlier turn is not a stall.
	StartedCard   bool
	lookupRetries int
	stopped       bool

	Steps        int
	MessageIndex int
	turnIndex    int
	Usage        provider.Usage
	PriorCost    float64
	Ended        string
	// transcript is what nova produced this turn, kept so the session's own messages_json survives a restart.
	transcript []message.Message
}

// hint is the automatic note that precedes her message. It is data, framed as data, and never an instruction.
func (s *RunState) hint() string {
	rating := "none"
	if s.Parsed.RatingQuarters() != 0 {
		rating = domain.FormatRating(s.Parsed.RatingQuarters())
	}
	date := s.Parsed.DateString()
	if date == "" {
		date = "none"
	}
	format := s.MediumFormat()
	if format == "" {
		format = "none"
	}
	medium := s.Medium
	if medium == "" {
		medium = "none"
	}
	intent := s.PreRead.Intent
	if intent == "" {
		intent = "none"
	}
	topics := "none"
	if len(s.PreRead.Topics) > 0 {
		topics = strings.Join(s.PreRead.Topics, ", ")
	}
	out := HintText()
	if out == "" {
		return "Automatic note, not an instruction. Her message is below."
	}
	tmpl, err := template.New("hint").Parse(out)
	if err != nil {
		return out
	}
	var buf bytes.Buffer
	_ = tmpl.Execute(&buf, map[string]string{
		"Intent": intent, "Medium": medium, "Format": format, "Rating": rating, "Date": date, "Topics": topics,
	})
	return buf.String()
}

// MediumFormat is the format to show: the first deterministic cue, else the pre-read's suggestion when it names
// one this app has a format for. "print" is deliberately empty, because it could be a hardback or a paperback.
func (s *RunState) MediumFormat() string {
	if format := s.Parsed.FormatString(); format != "" {
		return format
	}
	switch s.PreRead.Format {
	case domain.FormatCinema, domain.FormatStreaming, domain.FormatBroadcast, domain.FormatAudiobook,
		domain.FormatEbook, domain.FormatPhysical, domain.FormatLive:
		return s.PreRead.Format
	case "physical_media":
		return domain.FormatPhysical
	default:
		return ""
	}
}

// ask opens an interaction through the session engine and blocks on her answer. The engine writes the question
// and the answer; this function holds no database transaction while it waits.
func (s *RunState) ask(ctx context.Context, kind string, payload any) (json.RawMessage, error) {
	if s.Agent.responder == nil {
		return nil, ErrNoResponder
	}
	question, err := s.Agent.session.AskRound(ctx, s.SessionID, kind, payload)
	if err != nil {
		return nil, err
	}
	return s.Agent.responder.Wait(ctx, s.SessionID, question.ID)
}

// askAny opens a question that is not part of the bank, such as a fill_gap, still under the round cap.
func (s *RunState) askAny(ctx context.Context, kind string, payload any) (json.RawMessage, error) {
	if s.Agent.responder == nil {
		return nil, ErrNoResponder
	}
	question, err := s.Agent.session.Ask(ctx, s.SessionID, kind, payload)
	if err != nil {
		return nil, err
	}
	return s.Agent.responder.Wait(ctx, s.SessionID, question.ID)
}

// onMessage keeps what nova produced, without writing it anywhere yet. The whole transcript is stored once the
// turn ends, so a killed process loses no more than the turn it was in.
func (s *RunState) onMessage(m message.Message, _ *provider.Usage) error {
	if m.Synthetic || m.Role == message.RoleUser {
		return nil
	}
	s.transcript = append(s.transcript, m)
	return nil
}

// stallNudge returns the one nudge a turn that ended in prose should get, or "" when the turn did not stall. A
// turn that stopped for a budget or an error never stalls. Two shapes do:
//   - she picked a work and no card exists, so merge_candidates never ran;
//   - this turn started the card but the model never tried save_entry. A refused save counts as a try: a prose reply to a
//     refusal is the model talking to her, not a stall.
func (s *RunState) stallNudge() string {
	if s.Ended != "done" || s.stopped {
		return ""
	}
	if s.Chosen != nil && s.Draft == nil {
		return nudgePickInstruction
	}
	if s.Draft != nil && s.StartedCard && !s.SaveAttempted {
		return nudgeSaveInstruction
	}
	return ""
}

// guardCall bounds the number of tool steps and stops tools once the card has been built.
func (s *RunState) guardCall(_ context.Context, c novaagent.ToolCall) *novaagent.CallDecision {
	if len(c.Arguments) > maxToolArgs {
		return &novaagent.CallDecision{Block: true, Reason: "that tool call is too large"}
	}
	s.Steps++
	if s.Steps > s.Agent.maxSteps {
		s.stopped = true
		return &novaagent.CallDecision{Block: true, Reason: "the step limit for this turn has been reached"}
	}
	if s.SaveCalled && c.Name != "search_library" {
		return &novaagent.CallDecision{Block: true, Reason: "the card is already built; wait for her confirm"}
	}
	return nil
}

// checkInputBudget refuses a request whose transcript is already over the input cap. The estimate is deliberately
// rough (four characters a token) and errs high, which is the safe direction for a cap.
func (s *RunState) checkInputBudget(_ context.Context, msgs []message.Message) ([]message.Message, error) {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Content) + len(m.Reasoning)
		for _, call := range m.ToolCalls {
			chars += len(call.Function.Arguments)
		}
	}
	if chars/4 > s.Agent.maxInputTokens {
		return msgs, errBudget
	}
	return msgs, nil
}

// endTurn stops the loop once the card is built and the model has had its one closing sentence. It also stops a
// turn that hit the budget, with the reason already written.
func (s *RunState) endTurn(_ context.Context, t novaagent.Turn) novaagent.TurnDecision {
	if s.Ended == "budget" || s.stopped {
		return novaagent.TurnDecision{End: true}
	}
	if s.SaveCalled && len(t.Message.ToolCalls) == 0 {
		return novaagent.TurnDecision{End: true}
	}
	return novaagent.TurnDecision{}
}

// saveTranscript stores the conversation itself, which outlives the event log. User messages are the same ones
// the session already holds; the assistant and tool messages are this turn's.
func (s *RunState) saveTranscript(ctx context.Context, users []string) error {
	messages := make([]message.Message, 0, len(users)+len(s.transcript))
	for _, text := range users {
		messages = append(messages, message.User(text))
	}
	messages = append(messages, s.transcript...)
	body, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("agent: encode transcript: %w", err)
	}
	return s.Agent.store.SetMessages(ctx, s.SessionID, body)
}
