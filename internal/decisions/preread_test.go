package decisions

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/nova/decide"
	"github.com/AutumnsGrove/nova/provider"

	"github.com/AutumnsGrove/cairn/internal/world"
)

// fireClock fires a role's timer at once, so a timeout is tested without sleeping.
type fireClock struct{}

func (fireClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

// capture wraps the world's fake and keeps the request, which is where the state and instructions can be checked
// without logging anything.
type capture struct {
	inner decide.Decider
	reqs  []decide.Request
}

func (c *capture) Decide(ctx context.Context, req decide.Request) (*decide.Response, error) {
	c.reqs = append(c.reqs, req)
	return c.inner.Decide(ctx, req)
}

func prereadFixture(t *testing.T, d decide.Decider) (*PreReader, *Registry) {
	t.Helper()
	reg, err := Load("../../config/decisions")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return New(Config{Registry: reg, Decider: d}), reg
}

// script sends Dune through the pre-read: a book, read in print, with the story already covered.
func scriptDune(d *world.Decisions) {
	d.Set("intent", map[string]float64{"log_review": 0.93, "chitchat": 0.05})
	d.Set("medium", map[string]float64{"book": 0.81, "film": 0.14, "unknown": 0.05})
	d.Set("format", map[string]float64{"print": 0.90, "not_stated": 0.05})
	d.Set("revisit", map[string]float64{"no": 0.90, "yes": 0.10})
	d.Set("mentions_story", map[string]float64{"yes": 0.91, "no": 0.09})
	d.Set("mentions_ending", map[string]float64{"yes": 0.88, "no": 0.12})
}

func TestRunHappy(t *testing.T) {
	d := world.NewDecisions(world.NewFixedClock(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)))
	scriptDune(d)
	p, _ := prereadFixture(t, d)

	res, err := p.Run(context.Background(), Reading{
		Message:         "I finished Dune last night and the ending floored me.",
		PendingQuestion: "How was the pacing?",
		WorkTitle:       "Dune",
	}, Session{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Outcome.Intent != "log_review" || res.Outcome.Medium != "book" || res.Outcome.Format != "print" {
		t.Errorf("outcome = %+v", res.Outcome)
	}
	if len(res.Outcome.Topics) != 2 || res.Outcome.Topics[0] != "ending" || res.Outcome.Topics[1] != "story" {
		t.Errorf("topics = %v", res.Outcome.Topics)
	}
	if res.Usage.CostUSD != world.DecisionUsage.CostUSD {
		t.Errorf("usage = %+v, want the fake's cost", res.Usage)
	}
	if res.Model != "jev-1.14" {
		t.Errorf("model = %q", res.Model)
	}
	if len(res.Clips) != 0 {
		t.Errorf("clips = %+v, want none for a short message", res.Clips)
	}

	calls := d.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls recorded, want exactly one batched call", len(calls))
	}
	call := calls[0]
	wantSources := []string{SourceMessage, SourcePending, SourceWork}
	if strings.Join(call.Sources, ",") != strings.Join(wantSources, ",") {
		t.Errorf("sources = %v, want %v", call.Sources, wantSources)
	}
	// The fidelity check is a second call after the extract exists, so it must not be in this one.
	for _, name := range call.Question {
		if name == "extract_fidelity" {
			t.Error("extract_fidelity was sent in the first call")
		}
	}
	if len(call.Question) != 13 {
		t.Errorf("%d questions sent, want the 13 first-call checks: %v", len(call.Question), call.Question)
	}
}

// The state must arrive as separate sources, and every instruction must carry the shared preamble: the model is
// told what the app is on every question, because there is no system prompt on this wire.
func TestRunRequestShape(t *testing.T) {
	d := world.NewDecisions(nil)
	c := &capture{inner: d}
	p, reg := prereadFixture(t, c)

	if _, err := p.Run(context.Background(), Reading{Message: "I finished Dune."}, Session{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := c.reqs[0]
	sources, ok := req.State.([]decide.Source)
	if !ok {
		t.Fatalf("state = %T, want []decide.Source", req.State)
	}
	if len(sources) != 1 || sources[0].Source != SourceMessage {
		t.Errorf("sources = %+v, want only the message", sources)
	}
	// Roles resolves the role name to the pinned model before the backend sees it; a bare "cairn_preread"
	// reaching the wire would mean the decorator was skipped.
	if req.Model != "jev-1.14" {
		t.Errorf("model = %q, want the role's pinned model", req.Model)
	}
	intent := req.Questions["intent"]
	instr, _ := intent.Instructions.(string)
	if !strings.HasPrefix(instr, reg.Preamble) {
		t.Errorf("instructions do not start with the preamble: %q", instr)
	}
	if len(intent.Criteria) != len(reg.Checks["intent"].Options) {
		t.Errorf("criteria = %v, want every option", intent.Criteria)
	}
}

func TestRunUnconfigured(t *testing.T) {
	reg, err := Load("../../config/decisions")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := New(Config{Registry: reg})
	if _, err := p.Run(context.Background(), Reading{Message: "hello"}, Session{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Run = %v, want ErrNotConfigured", err)
	}
	if _, err := p.Fidelity(context.Background(), []string{"hello"}, "hello"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Fidelity = %v, want ErrNotConfigured", err)
	}
}

func TestRunClipsALongMessage(t *testing.T) {
	d := world.NewDecisions(nil)
	c := &capture{inner: d}
	p, _ := prereadFixture(t, c)

	long := strings.Repeat("a", 50_000)
	res, err := p.Run(context.Background(), Reading{Message: long, PendingQuestion: strings.Repeat("q", 5_000)}, Session{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Clips) != 2 {
		t.Fatalf("clips = %+v, want the message and the pending question", res.Clips)
	}
	bySource := map[string]Clip{}
	for _, clip := range res.Clips {
		bySource[clip.Source] = clip
	}
	if got := bySource[SourceMessage]; got.Original != 50_000 || got.Sent != MaxMessageChars {
		t.Errorf("message clip = %+v", got)
	}
	if got := bySource[SourcePending]; got.Original != 5_000 || got.Sent != MaxSourceChars {
		t.Errorf("pending clip = %+v", got)
	}
	sources := c.reqs[0].State.([]decide.Source)
	if len(sources[0].Text) != MaxMessageChars || len(sources[1].Text) != MaxSourceChars {
		t.Errorf("sent lengths = %d, %d", len(sources[0].Text), len(sources[1].Text))
	}
}

func TestRunEmptyMessage(t *testing.T) {
	d := world.NewDecisions(nil)
	p, _ := prereadFixture(t, d)
	var ue *ErrUnavailable
	if _, err := p.Run(context.Background(), Reading{}, Session{}); !errors.As(err, &ue) {
		t.Fatalf("Run = %v, want an unavailable", err)
	} else if ue.Reason != "empty message" {
		t.Errorf("reason = %q", ue.Reason)
	}
}

// Every way the backend can fail leaves the turn to carry on: an unavailable pre-read, never a blocked message.
func TestRunFaults(t *testing.T) {
	cases := map[string]struct {
		fault  world.DecisionFault
		clock  decide.Clock
		reason string
	}{
		"error":        {world.DecisionError, nil, "error"},
		"timeout":      {world.DecisionSlow, fireClock{}, "timeout"},
		"malformed":    {world.DecisionMalformed, nil, "malformed"},
		"inconsistent": {world.DecisionInconsistent, nil, "inconsistent"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := world.NewDecisions(nil)
			scriptDune(d)
			d.SetFault(tc.fault)
			p, _ := prereadFixture(t, d)
			p.roles = decide.NewRoles(decide.RolesConfig{
				Decider: d,
				Roles:   p.reg.Backend.DecideRoles(),
				Clock:   tc.clock,
			})

			for i := 0; i < 2; i++ {
				res, err := p.Run(context.Background(), Reading{Message: "I finished Dune."}, Session{})
				var ue *ErrUnavailable
				if !errors.As(err, &ue) {
					t.Fatalf("call %d = %v, want an unavailable", i+1, err)
				}
				if ue.Reason != tc.reason {
					t.Fatalf("call %d reason = %q, want %q", i+1, ue.Reason, tc.reason)
				}
				if res.Outcome.Intent != "" {
					t.Errorf("call %d routed on a failed pre-read: %+v", i+1, res.Outcome)
				}
			}
		})
	}
}

// A cancelled caller is the caller's business, not an unavailable backend: the turn was cancelled.
func TestRunCancelled(t *testing.T) {
	d := world.NewDecisions(nil)
	d.SetFault(world.DecisionSlow)
	p, _ := prereadFixture(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Run(ctx, Reading{Message: "I finished Dune."}, Session{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
}

func TestRunSessionBias(t *testing.T) {
	d := world.NewDecisions(nil)
	d.Set("intent", map[string]float64{"log_review": 0.70, "answer_pending": 0.20})
	p, _ := prereadFixture(t, d)
	res, err := p.Run(context.Background(), Reading{Message: "the ending"}, Session{Active: true, Intent: "answer_pending"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome.Intent != "answer_pending" {
		t.Errorf("intent = %q, want the session's", res.Outcome.Intent)
	}
}

func TestFidelity(t *testing.T) {
	d := world.NewDecisions(nil)
	d.Set("extract_fidelity", map[string]float64{"drops_opinion": 0.75, "faithful": 0.20, "adds_content": 0.05})
	p, _ := prereadFixture(t, d)

	res, err := p.Fidelity(context.Background(), []string{"I loved the ending, it floored me."}, "the ending floored me")
	if err != nil {
		t.Fatalf("Fidelity: %v", err)
	}
	if res.Verdict.Verdict != "drops_opinion" || res.Verdict.Bug {
		t.Errorf("verdict = %+v", res.Verdict)
	}
	if res.Usage.CostUSD != world.DecisionUsage.CostUSD {
		t.Errorf("usage = %+v", res.Usage)
	}
	call := d.Calls()[0]
	if strings.Join(call.Sources, ",") != SourceOriginal+","+SourceExtract {
		t.Errorf("sources = %v", call.Sources)
	}
	if strings.Join(call.Question, ",") != "extract_fidelity" {
		t.Errorf("questions = %v, want only the fidelity check", call.Question)
	}
}

// adds_content cannot happen once containment is proved in code, so it is a bug: logged, never a warning she sees.
func TestFidelityBugIsLoggedWithoutText(t *testing.T) {
	d := world.NewDecisions(nil)
	d.Set("extract_fidelity", map[string]float64{"adds_content": 0.90, "faithful": 0.05, "drops_opinion": 0.05})

	var logs bytes.Buffer
	reg, err := Load("../../config/decisions")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := New(Config{Registry: reg, Decider: d, Logger: slog.New(slog.NewTextHandler(&logs, nil))})

	const review = "I loved the ending, it floored me and no one else will."
	res, err := p.Fidelity(context.Background(), []string{review}, "the ending floored me")
	if err != nil {
		t.Fatalf("Fidelity: %v", err)
	}
	if !res.Verdict.Bug {
		t.Errorf("verdict = %+v, want logged as a bug", res.Verdict)
	}
	if res.Verdict.Verdict != "" {
		t.Errorf("verdict = %q, want no normal warning", res.Verdict.Verdict)
	}
	out := logs.String()
	if !strings.Contains(out, "bug") {
		t.Errorf("log = %q, want a bug line", out)
	}
	if strings.Contains(out, "floored") {
		t.Errorf("log carries review text: %q", out)
	}
}

func TestFidelityFaults(t *testing.T) {
	d := world.NewDecisions(nil)
	d.SetFault(world.DecisionError)
	p, _ := prereadFixture(t, d)
	var ue *ErrUnavailable
	if _, err := p.Fidelity(context.Background(), []string{"text"}, "text"); !errors.As(err, &ue) {
		t.Fatalf("Fidelity = %v, want an unavailable", err)
	}
}

func TestFidelityWithoutACheck(t *testing.T) {
	reg, err := LoadFiles([]byte(baseWording), []byte(basePolicy))
	if err != nil {
		t.Fatalf("LoadFiles: %v", err)
	}
	p := New(Config{Registry: reg, Decider: world.NewDecisions(nil)})
	var ue *ErrUnavailable
	if _, err := p.Fidelity(context.Background(), []string{"text"}, "text"); !errors.As(err, &ue) {
		t.Fatalf("Fidelity = %v, want an unavailable", err)
	}
}

// providerBudgets are the limits the clips must not cross.
func TestClipLimitsAreOrdered(t *testing.T) {
	if MaxMessageChars > MaxTotalChars || MaxSourceChars > MaxTotalChars || MaxBodyChars > MaxTotalChars {
		t.Fatalf("a per-source clip exceeds the total budget: %d, %d, %d, %d", MaxMessageChars, MaxSourceChars, MaxBodyChars, MaxTotalChars)
	}
	if MaxTotalChars > 96_000 {
		t.Errorf("total %d characters is more than the 24k-token context allows", MaxTotalChars)
	}
}

// The provider import is used by Result; this keeps the test file honest about its wiring.
var _ provider.Usage

// An unavailable pre-read carries a reason in her words and a cause for the log, and keeps the cause reachable.
func TestErrUnavailable(t *testing.T) {
	cause := errors.New("the backend said no")
	err := unavailable("timeout", cause)
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Error() = %q, want the reason", err)
	}
	if !errors.Is(err, cause) {
		t.Error("Unwrap lost the cause")
	}
	var ue *ErrUnavailable
	if !errors.As(errors.New("decisions: pre-read unavailable: timeout"), &ue) {
		// A plain Error() string is not an ErrUnavailable; this line documents that As is not satisfied by text.
		t.Log("a text-only error is not an *ErrUnavailable, as intended")
	}
}

// A clip must never send half a character: the extract is compared with her words, and a broken rune would be a
// difference that is not hers.
func TestClipTextKeepsRunesWhole(t *testing.T) {
	text := "éééé"
	if got := clipText(text, 3); got != "é" {
		t.Errorf("clipText(%q, 3) = %q, want one whole rune", text, got)
	}
	if got := clipText(text, 1); got != "" {
		t.Errorf("clipText(%q, 1) = %q, want nothing rather than half a rune", text, got)
	}
	if got := clipText("abc", 10); got != "abc" {
		t.Errorf("clipText short = %q", got)
	}
}
