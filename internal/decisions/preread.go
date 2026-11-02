package decisions

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/AutumnsGrove/nova/decide"
	"github.com/AutumnsGrove/nova/provider"
)

// The source names sent with a call. Each source stays separate on the wire, so the model can tell her message
// from the question it asked and from the work's title (PLAN.md 2, docs/decisions).
const (
	SourceMessage  = "message"
	SourcePending  = "pending_question"
	SourceWork     = "work"
	SourceOriginal = "original"
	SourceExtract  = "extract"
)

// The clip limits from docs/STANDARDS.md 4a. The decision model takes text only and has a 32k-token context;
// overrun is a clean error, not truncation, so the clip is deliberate, bounded and recorded.
const (
	MaxMessageChars = 12_000
	MaxSourceChars  = 2_000
	MaxBodyChars    = 8_000
	MaxTotalChars   = 24_000
)

// Reading is one message and the two pieces of context that make a reply readable.
type Reading struct {
	Message         string
	PendingQuestion string
	WorkTitle       string
}

// Clip records that a source was shortened before it was sent. It holds lengths only: never the text.
type Clip struct {
	Source   string
	Original int
	Sent     int
}

// Result is one pre-read: the routes policy made of the odds, the odds themselves, and what the call cost.
type Result struct {
	Outcome Outcome
	Odds    map[string]Odds
	Clips   []Clip
	Usage   provider.Usage
	Model   string
}

// FidelityResult is the second call's outcome. It can only ever produce a warning.
type FidelityResult struct {
	Verdict Verdict
	Odds    Odds
	Clips   []Clip
	Usage   provider.Usage
	Model   string
}

// Config wires the pre-read. A nil Decider is the normal "decisions is off" state, not an error
// (docs/BUILD.md invariant 13).
type Config struct {
	Registry *Registry
	Decider  decide.Decider
	// Clock drives the role's timeout, so a test proves the deadline without sleeping.
	Clock decide.Clock
	// Logger receives the fidelity bug and nothing else. Nil means discard.
	Logger *slog.Logger
}

// PreReader runs the pre-read and the fidelity check over nova's decide contract.
type PreReader struct {
	reg   *Registry
	roles *decide.Roles
	log   *slog.Logger
}

// New builds the pre-read. It never fails: a missing registry or decider surfaces as ErrNotConfigured when the
// caller asks for a read, which keeps a turn running when the layer is off.
func New(cfg Config) *PreReader {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	p := &PreReader{reg: cfg.Registry, log: logger}
	if cfg.Registry != nil && cfg.Decider != nil {
		p.roles = decide.NewRoles(decide.RolesConfig{
			Decider: cfg.Decider,
			Roles:   cfg.Registry.Backend.DecideRoles(),
			Clock:   cfg.Clock,
		})
	}
	return p
}

// DecideRoles converts the config's roles into nova's table. It is the one place the two shapes meet, so a field
// added to either is a compile error here rather than a silently ignored timeout.
func (b Backend) DecideRoles() map[string]decide.Role {
	out := make(map[string]decide.Role, len(b.Roles))
	for name, role := range b.Roles {
		out[name] = decide.Role{Model: role.Model, Fallback: role.Fallback, Timeout: role.Timeout}
	}
	return out
}

// Run makes the one batched call and applies policy. On any failure it returns an *ErrUnavailable and a Result
// carrying the clips and the cost so far, so the turn can proceed with a notice (S05 "sad" route).
func (p *PreReader) Run(ctx context.Context, reading Reading, session Session) (Result, error) {
	if p == nil || p.reg == nil || p.roles == nil {
		return Result{}, ErrNotConfigured
	}

	sources, clips, err := clipSources([]namedSource{
		{name: SourceMessage, text: reading.Message, limit: MaxMessageChars},
		{name: SourcePending, text: reading.PendingQuestion, limit: MaxSourceChars},
		{name: SourceWork, text: reading.WorkTitle, limit: MaxSourceChars},
	})
	if err != nil {
		return Result{}, err
	}

	req := decide.Request{
		Model:     RolePreread,
		State:     sources,
		Questions: p.questions(p.reg.Before()),
	}
	resp, err := p.roles.Decide(ctx, req)
	if err != nil {
		return Result{Clips: clips, Usage: usageOf(resp), Model: modelOf(resp)}, classify(ctx, err)
	}
	if err := decide.ValidateResponse(req, resp); err != nil {
		return Result{Clips: clips, Usage: resp.Usage, Model: resp.Model}, unavailable("malformed", err)
	}
	answers := oddsOf(resp)
	if err := p.reg.CheckOdds(answers); err != nil {
		return Result{Clips: clips, Usage: resp.Usage, Model: resp.Model}, unavailable("inconsistent", err)
	}

	return Result{
		Outcome: p.reg.Apply(answers, session),
		Odds:    answers,
		Clips:   clips,
		Usage:   resp.Usage,
		Model:   resp.Model,
	}, nil
}

// Fidelity compares her original words with the extract proposed as her body. It runs after the extract exists,
// as its own call, and its result is only ever a warning (S05.7).
func (p *PreReader) Fidelity(ctx context.Context, original []string, extract string) (FidelityResult, error) {
	if p == nil || p.reg == nil || p.roles == nil {
		return FidelityResult{}, ErrNotConfigured
	}
	checks := p.reg.Post()
	if len(checks) != 1 {
		return FidelityResult{}, unavailable("no fidelity check", nil)
	}
	check := checks[0]

	sources, clips, err := clipSources([]namedSource{
		{name: SourceOriginal, text: strings.Join(original, "\n\n"), limit: MaxTotalChars},
		{name: SourceExtract, text: extract, limit: MaxBodyChars},
	})
	if err != nil {
		return FidelityResult{Clips: clips}, err
	}

	req := decide.Request{
		Model:     RoleFidelity,
		State:     sources,
		Questions: p.questions([]*Check{check}),
	}
	resp, err := p.roles.Decide(ctx, req)
	if err != nil {
		return FidelityResult{Clips: clips, Usage: usageOf(resp), Model: modelOf(resp)}, classify(ctx, err)
	}
	if err := decide.ValidateResponse(req, resp); err != nil {
		return FidelityResult{Clips: clips, Usage: resp.Usage, Model: resp.Model}, unavailable("malformed", err)
	}
	answers := oddsOf(resp)
	if err := p.reg.CheckPostOdds(answers); err != nil {
		return FidelityResult{Clips: clips, Usage: resp.Usage, Model: resp.Model}, unavailable("inconsistent", err)
	}

	verdict := check.FidelityVerdict(answers[check.Name])
	if verdict.Bug {
		// adds_content means the code that proved containment is wrong. It is a bug with a stable line and no
		// text, and it is never shown to her as a normal warning.
		p.log.Error("fidelity reported content the original does not contain",
			"check", check.Name, "bug", true, "sources", SourceOriginal+","+SourceExtract)
	}
	return FidelityResult{
		Verdict: verdict,
		Odds:    answers[check.Name],
		Clips:   clips,
		Usage:   resp.Usage,
		Model:   resp.Model,
	}, nil
}

// questions builds the wire questions: the shared preamble, then the check's own instructions, and every option
// as a criterion. The preamble is repeated per question because this wire has no system prompt.
func (p *PreReader) questions(checks []*Check) map[string]decide.Question {
	out := make(map[string]decide.Question, len(checks))
	for _, c := range checks {
		criteria := make(map[string]string, len(c.Options))
		for option, description := range c.Options {
			criteria[option] = description
		}
		out[c.Name] = decide.Question{
			Kind:         decide.KindChoice,
			Instructions: p.reg.Preamble + "\n\n" + c.Instructions,
			Criteria:     criteria,
		}
	}
	return out
}

// namedSource is one source before clipping.
type namedSource struct {
	name  string
	text  string
	limit int
}

// clipSources trims each source to its own limit and the whole state to the total budget, recording every cut.
// An empty message with no context is nothing to read, which is its own reason.
func clipSources(items []namedSource) ([]decide.Source, []Clip, error) {
	empty := true
	for _, item := range items {
		if strings.TrimSpace(item.text) != "" {
			empty = false
			break
		}
	}
	if empty {
		return nil, nil, unavailable("empty message", nil)
	}

	total := MaxTotalChars
	sources := make([]decide.Source, 0, len(items))
	var clips []Clip
	for _, item := range items {
		if strings.TrimSpace(item.text) == "" {
			continue
		}
		text := item.text
		limit := item.limit
		if limit > total {
			limit = total
		}
		if len(text) > limit {
			text = clipText(text, limit)
			clips = append(clips, Clip{Source: item.name, Original: len(item.text), Sent: len(text)})
		}
		total -= len(text)
		sources = append(sources, decide.Source{Source: item.name, Text: text})
	}
	if len(sources) == 0 {
		return nil, clips, unavailable("empty message", nil)
	}
	return sources, clips, nil
}

// clipText cuts at a rune boundary, so a clip never sends half a character.
func clipText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// oddsOf converts the wire's answers into the policy engine's shape.
func oddsOf(resp *decide.Response) map[string]Odds {
	out := make(map[string]Odds, len(resp.Answers))
	for name, answer := range resp.Answers {
		probabilities := make(map[string]float64, len(answer.Probabilities))
		for option, p := range answer.Probabilities {
			probabilities[option] = p
		}
		out[name] = Odds{Choice: answer.Choice, Probabilities: probabilities, Confidence: answer.Confidence}
	}
	return out
}

// classify turns a transport failure into a reason the UI can say in plain words. The caller's own cancellation
// is returned untouched, because a cancelled turn is not an unavailable backend.
func classify(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var pe *provider.Error
	if errors.As(err, &pe) && pe.Code == "timeout" {
		return unavailable("timeout", err)
	}
	return unavailable("error", err)
}

// usageOf reads the cost a failed attempt still reported, so a timeout is not invisible in the ledger.
func usageOf(resp *decide.Response) provider.Usage {
	if resp == nil {
		return provider.Usage{}
	}
	return resp.Usage
}

// modelOf reads the build that answered, which a benchmark records beside the odds.
func modelOf(resp *decide.Response) string {
	if resp == nil {
		return ""
	}
	return resp.Model
}
