// Package agent wires the model into Cairn, safely: nova's loop, Cairn's tools, the pipeline that turns a message
// into a draft card, and the guardrails that are enforced in code rather than in a prompt
// (docs/slices/S08-agent-pipeline.md).
//
// The shape is the one docs/INTEGRATION.md calls the embedding. Nova owns the loop; Cairn owns the tools, the
// session, the draft and the event vocabulary. Every tool reads its dependency bundle out of tool.Context.Data,
// and the model never sees a type of Cairn's.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	novaagent "github.com/AutumnsGrove/nova/agent"
	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"
	"github.com/AutumnsGrove/nova/provider/openrouter"
	"github.com/AutumnsGrove/nova/tool"

	"github.com/AutumnsGrove/cairn/internal/banks"
	"github.com/AutumnsGrove/cairn/internal/decisions"
	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/providers"
	"github.com/AutumnsGrove/cairn/internal/session"
	"github.com/AutumnsGrove/cairn/internal/store"
)

// PinnedModel is the chat model from docs/BUILD.md 2. It is pinned: an alias moves, and then every recorded cost
// and eval measures a different model.
const PinnedModel = "deepseek/deepseek-v4.1-flash-1017"

// Limits from docs/STANDARDS.md 4a. They are the defaults; a test may lower them to prove the boundary.
const (
	MaxToolSteps    = 12
	MaxInputTokens  = 60_000
	MaxOutputTokens = 8_000
	TurnDeadline    = 120 * time.Second
)

// ErrModelOff means the provider has no key. It is a normal runtime state, not a broken turn: the message is kept
// and the card can be filled in by hand (docs/BUILD.md invariant 13).
var ErrModelOff = errors.New("agent: the model is not set up")

// ErrNoResponder means nothing can ask her a question in this process. The tool says so instead of hanging.
var ErrNoResponder = errors.New("agent: there is no one to ask")

// Responder asks her one question and waits for the answer. It is the seam S09 fills with the socket: the agent
// opens the interaction through the session engine, then blocks here without holding a database transaction.
type Responder interface {
	Wait(ctx context.Context, sessionID, interactionID string) (json.RawMessage, error)
}

// Config wires the agent. Store, Session and Finder are the live dependencies; Banks and Decisions may be nil,
// which is the degraded state and never a startup error.
type Config struct {
	Store     *store.Store
	Session   *session.Engine
	Finder    *providers.Finder
	Banks     *banks.Set
	Decisions *decisions.PreReader

	// Model is the pinned chat model. Empty means PinnedModel.
	Model string
	// APIKey enables the model. Empty means off, and every turn degrades to a kept message.
	APIKey string
	// BaseURL overrides OpenRouter, for the world in a test. Empty means the real endpoint.
	BaseURL string
	// Provider is a test seam. When set it replaces the OpenRouter client the key would build.
	Provider provider.Provider

	// Responder is how ask_reviewer and the lookup-failed card reach her. Nil means questions are refused.
	Responder Responder
	// Live receives the events the protocol does not persist, such as activity and cost_update. S09 publishes
	// them on the socket; a test records them.
	Live func(Event)

	Logger *slog.Logger
	Now    func() time.Time
	NewID  func() string

	// The caps. Zero means the default above.
	MaxSteps        int
	MaxInputTokens  int
	MaxOutputTokens int
	TurnDeadline    time.Duration
}

// Event is one live-only session event. It has no sequence number because it is never replayed.
type Event struct {
	SessionID string
	Kind      string
	Data      any
}

// Agent runs one turn at a time. A turn is a message in, a card out; the session engine owns the state between
// turns, so two agents over one store would still be safe (the server has one).
type Agent struct {
	store     *store.Store
	session   *session.Engine
	finder    *providers.Finder
	banks     *banks.Set
	decisions *decisions.PreReader
	provider  provider.Provider
	model     string
	tools     *tool.Registry
	responder Responder
	live      func(Event)
	log       *slog.Logger
	now       func() time.Time
	newID     func() string

	maxSteps        int
	maxInputTokens  int
	maxOutputTokens int
	turnDeadline    time.Duration
}

// New builds the agent and validates the provider pin. A bad model id is a startup error, never a turn that runs
// on something else.
func New(cfg Config) (*Agent, error) {
	if cfg.Store == nil {
		return nil, errors.New("agent: a store is required")
	}
	if cfg.Session == nil {
		return nil, errors.New("agent: a session engine is required")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = PinnedModel
	}
	if err := validatePin(model); err != nil {
		return nil, err
	}

	a := &Agent{
		store: cfg.Store, session: cfg.Session, finder: cfg.Finder, banks: cfg.Banks, decisions: cfg.Decisions,
		model: model, responder: cfg.Responder, live: cfg.Live, now: cfg.Now, newID: cfg.NewID,
		maxSteps:        firstPositive(cfg.MaxSteps, MaxToolSteps),
		maxInputTokens:  firstPositive(cfg.MaxInputTokens, MaxInputTokens),
		maxOutputTokens: firstPositive(cfg.MaxOutputTokens, MaxOutputTokens),
		turnDeadline:    firstDuration(cfg.TurnDeadline, TurnDeadline),
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.newID == nil {
		a.newID = func() string { return fmt.Sprintf("ag-%d", time.Now().UnixNano()) }
	}
	a.log = cfg.Logger
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}

	switch {
	case cfg.Provider != nil:
		a.provider = cfg.Provider
	case cfg.APIKey != "":
		a.provider = openrouter.New(openrouter.Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL})
	}
	a.tools = a.newTools()
	return a, nil
}

// Model is the id this agent runs. It is read by the version screen and by an eval report.
func (a *Agent) Model() string { return a.model }

// TurnInput is one message from the client. The date and zone are the phone's, because "last night" means where
// she is, not where the potato is (docs/PROTOCOL.md 5).
type TurnInput struct {
	SessionID   string
	Text        string
	ClientMsgID string
	ClientDate  string
	Timezone    string
}

// Outcome is what one turn left behind. A non-nil error is a real failure; a degraded turn has Ended set and a
// persisted notice, and the message is always kept.
type Outcome struct {
	Draft            *session.Draft
	Ended            string
	Steps            int
	CostUSD          float64
	PromptTokens     int
	CompletionTokens int
	// Revises is the saved entry a revision draft is editing, for the confirm.
	Revises string
}

// HandleMessage stores her message, runs the pre-read and the deterministic parse, then drives nova until the
// card is built or the turn ends. It never commits an entry: save_entry builds the draft, and her confirm is the
// only commit (docs/BUILD.md invariant 2).
func (a *Agent) HandleMessage(ctx context.Context, in TurnInput) (Outcome, error) {
	if strings.TrimSpace(in.Text) == "" {
		return Outcome{}, &session.Refusal{Code: session.CodeInvalidValue, Message: "There is nothing in that message."}
	}
	if in.ClientMsgID != "" {
		if _, seen, err := a.session.Seen(ctx, in.SessionID, in.ClientMsgID); err != nil {
			return Outcome{}, err
		} else if seen {
			return Outcome{}, store.ErrDuplicateClientMessage
		}
	}
	if _, err := a.session.AppendUserMessage(ctx, in.SessionID, in.Text, in.ClientMsgID); err != nil {
		return Outcome{}, err
	}

	messages, err := a.session.UserMessages(ctx, in.SessionID)
	if err != nil {
		return Outcome{}, err
	}
	sourceText := strings.Join(messages, "\n")
	if len(sourceText) > domain.MaxSpanSource {
		// The locator refuses a source this large rather than clipping it, so the turn still runs but cannot
		// build a body from it. She can type the body on the card.
		if err := a.notice(ctx, in.SessionID, "warn", "message_too_long_for_extraction",
			"These messages are too long to quote from. You can type the review on the card."); err != nil {
			return Outcome{}, err
		}
	}

	state := &RunState{
		Agent: a, SessionID: in.SessionID, Text: in.Text, SourceText: sourceText,
		MessageIndex: len(messages) - 1,
		Parsed:       parseMessage(in.Text, a.now(), in.ClientDate, in.Timezone),
	}
	if err := a.preread(ctx, state); err != nil {
		return Outcome{}, err
	}
	if err := a.resolveMedium(ctx, state); err != nil {
		return Outcome{}, err
	}

	// A card may already exist from an earlier turn; the state reads it so the model sees the work and the
	// parsed fields it already has.
	if draft, err := a.session.Draft(ctx, in.SessionID); err == nil {
		state.Draft = &draft
		if applyErr := a.applyParsed(ctx, state); applyErr != nil {
			return Outcome{}, applyErr
		}
	} else if !errors.Is(err, session.ErrNoDraft) {
		return Outcome{}, err
	}

	if a.provider == nil {
		if err := a.notice(ctx, in.SessionID, "warn", "model_off",
			"The model is not set up. Your words are kept, and you can fill in the card by hand."); err != nil {
			return Outcome{}, err
		}
		return a.finish(ctx, state, "model_off")
	}

	runErr := a.runModel(ctx, state)
	if runErr != nil {
		// A model failure is a sad route, not a lost message: the notice says so and the session stays open.
		code, text := "model_unavailable",
			"The model did not finish. Your words are kept; you can try again or fill in the card by hand."
		switch {
		case errors.Is(runErr, errBudget):
			code, text = "turn_budget", "This turn got long, so I stopped. Your words are kept."
		case errors.Is(runErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			code, text = "turn_timeout", "The model took longer than two minutes. Your words are kept; try again."
		}
		if err := a.notice(ctx, in.SessionID, "warn", code, text); err != nil {
			return Outcome{}, err
		}
		a.log.Warn("agent turn failed", "session", in.SessionID, "reason", runErr.Error())
	}
	if state.stopped && runErr == nil {
		if err := a.notice(ctx, in.SessionID, "warn", "step_limit",
			"I stopped after several steps. The card is as far as I got; you can fill in the rest."); err != nil {
			return Outcome{}, err
		}
	}

	return a.finish(ctx, state, state.Ended)
}

// runModel drives nova's loop over the session's transcript and the automatic note.
func (a *Agent) runModel(ctx context.Context, state *RunState) error {
	history, prompt, err := a.transcript(ctx, state)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, a.turnDeadline)
	defer cancel()

	// The ledgers are receipts, not gates: a read failure costs a number on the "why" sheet, not the turn.
	prior, err := a.store.SessionCost(ctx, state.SessionID)
	if err != nil {
		a.log.Error("session cost read failed", "session", state.SessionID, "err", err.Error())
		prior = 0
	}
	state.PriorCost = prior
	turns, err := a.store.TurnCalls(ctx, state.SessionID)
	if err != nil {
		a.log.Error("turn ledger read failed", "session", state.SessionID, "err", err.Error())
	}
	state.turnIndex = len(turns) + 1

	tr := &translator{ctx: ctx, agent: a, state: state, live: a.live}
	cfg := novaagent.Config{
		Provider:       a.provider,
		Model:          a.model,
		System:         SystemPrompt(),
		Tools:          a.tools,
		ToolData:       state,
		Emit:           tr.Emit,
		OnMessage:      state.onMessage,
		MaxTurns:       a.maxSteps + 2,
		MaxTokens:      a.maxOutputTokens,
		ToolExecution:  novaagent.Sequential,
		RequestTimeout: a.turnDeadline,
		WrapUp:         &novaagent.WrapUp{Instruction: wrapUpInstruction, Fallback: wrapUpFallback},
		Hooks: novaagent.Hooks{
			ToolCall:   state.guardCall,
			BeforeTurn: state.checkInputBudget,
			TurnEnd:    state.endTurn,
		},
	}
	start := a.now()
	res, runErr := novaagent.New(cfg).Run(ctx, history, prompt)
	elapsed := a.now().Sub(start)

	state.Ended = "done"
	if res != nil {
		state.Usage = res.Usage
		if res.Reason != "" {
			state.Ended = res.Reason
		}
	}
	if runErr != nil {
		state.Ended = "error"
		if errors.Is(runErr, errBudget) {
			state.Ended = "budget"
		}
	}
	// The ledger and the transcript are receipts for a turn that already happened, so they get their own short
	// context: a timed-out run must still be recorded, and a cancelled caller must not erase the cost.
	recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer recordCancel()
	if users, userErr := a.session.UserMessages(recordCtx, state.SessionID); userErr == nil {
		if saveErr := state.saveTranscript(recordCtx, users); saveErr != nil {
			a.log.Error("transcript write failed", "session", state.SessionID, "err", saveErr.Error())
		}
	}
	if recordErr := a.store.RecordTurn(recordCtx, domain.TurnCall{
		SessionID: state.SessionID, TurnIndex: state.turnIndex, Model: a.model,
		PromptTokens: state.Usage.PromptTokens, CompletionTokens: state.Usage.CompletionTokens,
		CostUSD: state.Usage.CostUSD, LatencyMS: int(elapsed.Milliseconds()), Reason: state.Ended,
	}); recordErr != nil {
		a.log.Error("turn ledger write failed", "session", state.SessionID, "err", recordErr.Error())
	}
	return runErr
}

// transcript builds the history and the prompt. The stored transcript carries the previous turns, so a second
// message in the same session sees what the model already said and did. The automatic note goes immediately
// before her latest message, framed as data; the message itself is the prompt.
func (a *Agent) transcript(ctx context.Context, state *RunState) ([]message.Message, message.Message, error) {
	messages, err := a.session.UserMessages(ctx, state.SessionID)
	if err != nil {
		return nil, message.Message{}, err
	}
	if len(messages) == 0 {
		return nil, message.Message{}, errors.New("agent: the session has no message to answer")
	}
	sess, err := a.session.Session(ctx, state.SessionID)
	if err != nil {
		return nil, message.Message{}, err
	}
	var history []message.Message
	if len(sess.Messages) > 0 {
		if err := json.Unmarshal(sess.Messages, &history); err != nil {
			return nil, message.Message{}, fmt.Errorf("agent: read the session transcript: %w", err)
		}
	}
	if len(history) == 0 {
		for _, text := range messages[:len(messages)-1] {
			history = append(history, message.User(text))
		}
	}
	history = append(history, message.Message{Role: message.RoleUser, Content: state.hint(), Synthetic: true})
	prompt := message.User(messages[len(messages)-1])
	return history, prompt, nil
}

// finish records the running cost, emits the live total and returns the card if there is one.
func (a *Agent) finish(ctx context.Context, state *RunState, ended string) (Outcome, error) {
	out := Outcome{
		Ended: ended, Steps: state.Steps, CostUSD: state.Usage.CostUSD,
		PromptTokens: state.Usage.PromptTokens, CompletionTokens: state.Usage.CompletionTokens,
		Revises: state.Revises,
	}
	if draft, err := a.session.Draft(ctx, state.SessionID); err == nil {
		out.Draft = &draft
	} else if !errors.Is(err, session.ErrNoDraft) {
		return out, err
	}
	if total, err := a.store.SessionCost(ctx, state.SessionID); err != nil {
		a.log.Error("session cost read failed", "session", state.SessionID, "err", err.Error())
	} else if a.live != nil {
		a.live(Event{SessionID: state.SessionID, Kind: "cost_update", Data: map[string]any{"session_total_usd": total}})
	}
	return out, nil
}

// notice writes one persisted notice, which is how a degraded path explains itself in plain words.
func (a *Agent) notice(ctx context.Context, sessionID, level, code, text string) error {
	_, err := a.session.Append(ctx, sessionID, store.EventNotice, map[string]any{
		"level": level, "code": code, "text": text,
	})
	return err
}

// validatePin refuses an empty id, a role name and the alias spellings. It is the same rule config applies to
// CAIRN_MODEL, repeated here because an embedding caller can bypass the environment.
func validatePin(model string) error {
	if model == "" {
		return errors.New("agent: the model is required")
	}
	if strings.HasSuffix(model, "-latest") || strings.HasPrefix(model, "~") {
		return fmt.Errorf("agent: %q is an alias; the model must be pinned", model)
	}
	if !strings.Contains(model, "/") {
		return fmt.Errorf("agent: %q is not a provider/model id", model)
	}
	return nil
}

func firstPositive(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func firstDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
