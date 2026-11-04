// Command cairn-eval runs the fixture corpus against the real model and the real decisions backend.
//
// It is the operator's live check at G4, never CI: it needs CAIRN_OPENROUTER_API_KEY and spends money. The CI
// runner is the same corpus against the fake model (internal/agent/corpus_test.go), so the only difference here is
// where the answers come from.
//
// It writes a JSON report with the odds table, what the real model did, and where the card differed from the
// invented expectation. A threshold is not tuned from this report until Autumn reads it (docs/BUILD.md 6, G4).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	decideor "github.com/AutumnsGrove/nova/decide/openrouter"

	"github.com/AutumnsGrove/cairn/internal/agent"
	"github.com/AutumnsGrove/cairn/internal/banks"
	"github.com/AutumnsGrove/cairn/internal/config"
	"github.com/AutumnsGrove/cairn/internal/decisions"
	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/providers"
	"github.com/AutumnsGrove/cairn/internal/session"
	"github.com/AutumnsGrove/cairn/internal/store"
)

const usage = `cairn-eval: run the S08 corpus against the real model.

  cairn-eval [corpus.json]     default: internal/agent/testdata/corpus-live.json

Needs CAIRN_OPENROUTER_API_KEY. A TMDB key enables the film and TV cases; without one those cases report the
provider as off. The report goes to stdout as JSON.
`

func main() {
	os.Exit(run(os.Args[1:], os.Environ(), os.Stdout, os.Stderr))
}

func run(args []string, environ []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(stdout, usage)
		return 0
	}
	cfg, err := config.Load(environ)
	if err != nil {
		fmt.Fprintf(stderr, "cairn-eval: %v\n", err)
		return 1
	}
	if cfg.OpenRouterKey == "" {
		fmt.Fprintln(stderr, "cairn-eval: CAIRN_OPENROUTER_API_KEY is not set; this command spends money and is yours to run")
		return 2
	}
	corpusPath := "internal/agent/testdata/corpus-live.json"
	if len(args) > 0 {
		corpusPath = args[0]
	}
	cases, err := agent.LoadCorpus(corpusPath)
	if err != nil {
		fmt.Fprintf(stderr, "cairn-eval: %v\n", err)
		return 1
	}
	set, err := banks.LoadDir("config/questions")
	if err != nil {
		fmt.Fprintf(stderr, "cairn-eval: %v\n", err)
		return 1
	}
	reg, err := decisions.Load("config/decisions")
	if err != nil {
		fmt.Fprintf(stderr, "cairn-eval: %v\n", err)
		return 1
	}
	pre := decisions.New(decisions.Config{
		Registry: reg, Decider: decideor.New(decideor.Config{APIKey: cfg.OpenRouterKey}),
	})
	temp, err := os.MkdirTemp("", "cairn-eval-*")
	if err != nil {
		fmt.Fprintf(stderr, "cairn-eval: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(temp) }()
	finder := providers.NewFinder(providers.Config{
		TMDBKey: cfg.TMDBKey, DataDir: temp, UserAgent: "cairn-eval (Cairn's S08 corpus)",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	report := agent.RunCorpus(ctx, cases, func(ctx context.Context, c agent.Case) agent.CaseResult {
		return runCase(ctx, c, cfg, set, pre, finder, temp)
	})

	thresholds := make(map[string]float64, len(reg.Checks))
	for name, check := range reg.Checks {
		thresholds[name] = check.Threshold
	}
	out := map[string]any{
		"summary": report.Summary(), "cases": report.Cases, "passed": report.Passed,
		"failed": report.Failed, "odds": report.Odds, "thresholds": thresholds,
		"model": cfg.Model, "note": "real-model run; read it before tuning any threshold (G4)",
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(out); err != nil {
		fmt.Fprintf(stderr, "cairn-eval: %v\n", err)
		return 1
	}
	if len(report.Failed) > 0 {
		return 1
	}
	return 0
}

// runCase drives one case through the real pipeline. The model chooses its own tools; the responder answers the
// questions a person would, deterministically, so the run can finish unattended.
func runCase(ctx context.Context, c agent.Case, cfg config.Config, set *banks.Set,
	pre *decisions.PreReader, finder *providers.Finder, dir string,
) agent.CaseResult {
	result := agent.CaseResult{}
	fail := func(format string, args ...any) {
		result.Failures = append(result.Failures, fmt.Sprintf(format, args...))
	}

	db, err := store.Open(ctx, store.Options{Path: filepath.Join(dir, c.ID+".db")})
	if err != nil {
		fail("store: %v", err)
		return result
	}
	defer func() { _ = db.Close() }()
	if migrateErr := db.Migrate(ctx); migrateErr != nil {
		fail("migrate: %v", migrateErr)
		return result
	}
	engine := session.New(session.Config{Store: db, ServerVersion: "eval"})
	responder := &staticResponder{session: engine, store: db, c: c}
	a, err := agent.New(agent.Config{
		Store: db, Session: engine, Finder: finder, Banks: set, Decisions: pre,
		Model: cfg.Model, APIKey: cfg.OpenRouterKey, Responder: responder,
	})
	if err != nil {
		fail("agent: %v", err)
		return result
	}

	sess, err := engine.CreateEntry(ctx)
	if err != nil {
		fail("create session: %v", err)
		return result
	}
	out, err := a.HandleMessage(ctx, agent.TurnInput{
		SessionID: sess.ID, Text: c.Message, ClientMsgID: "eval-" + c.ID,
	})
	if err != nil {
		fail("handle message: %v", err)
		return result
	}
	// The agent's persisted pre-read is the odds table; a second call here would spend money twice and could
	// disagree with the answer the pipeline actually used.
	result.Odds = prereadOdds(ctx, db, sess.ID)
	if c.Expected.NoCard {
		if out.Draft != nil {
			fail("a card was built, want none")
		}
		return result
	}
	if out.Draft == nil {
		fail("no card (ended %s, steps %d, cost $%.4f): %s", out.Ended, out.Steps, out.CostUSD, diagnose(ctx, db, engine, sess.ID))
		return result
	}
	entry := out.Draft.Entry
	if entry.Status != domain.StatusDraft {
		fail("status = %q", entry.Status)
	}
	if entry.Rating != c.Expected.Rating {
		fail("rating = %d, want %d", entry.Rating, c.Expected.Rating)
	}
	if entry.FinishedOn != c.Expected.Date {
		fail("date = %q, want %q", entry.FinishedOn, c.Expected.Date)
	}
	if entry.Format != c.Expected.Format {
		fail("format = %q, want %q", entry.Format, c.Expected.Format)
	}
	if entry.Body != c.Expected.Body {
		fail("body = %q, want %q", entry.Body, c.Expected.Body)
	}
	if entry.Verdict != c.Expected.Verdict {
		fail("verdict = %q, want %q", entry.Verdict, c.Expected.Verdict)
	}
	if c.Expected.WorkTitle != "" {
		if work, workErr := db.Work(ctx, entry.WorkID); workErr != nil {
			fail("work: %v", workErr)
		} else if work.Title != c.Expected.WorkTitle {
			fail("work title = %q, want %q", work.Title, c.Expected.WorkTitle)
		}
	}
	if c.Expected.Revises != (out.Revises != "") {
		fail("revises = %q, want a revision: %v", out.Revises, c.Expected.Revises)
	}
	if len(result.Failures) > 0 {
		result.Failures = append(result.Failures, "diagnosis: "+diagnose(ctx, db, engine, sess.ID))
	}
	return result
}

// prereadOdds reads the odds the agent wrote for this turn.
func prereadOdds(ctx context.Context, db *store.Store, sessionID string) map[string]map[string]float64 {
	events, err := db.EventsSince(ctx, sessionID, 0)
	if err != nil {
		return nil
	}
	for _, ev := range events {
		if ev.Kind != store.EventPreread {
			continue
		}
		var payload struct {
			Odds map[string]map[string]float64 `json:"odds"`
		}
		if json.Unmarshal(ev.Payload, &payload) == nil {
			return payload.Odds
		}
	}
	return nil
}

// diagnose explains a missing card with the tool order, the notices and the model's last sentence. It prints no
// review text beyond what the model itself said, and the report is the operator's to keep outside the repo.
func diagnose(ctx context.Context, db *store.Store, engine *session.Engine, sessionID string) string {
	events, err := db.EventsSince(ctx, sessionID, 0)
	if err != nil {
		return "no events: " + err.Error()
	}
	var tools, notices []string
	for _, ev := range events {
		switch ev.Kind {
		case store.EventToolStart:
			var payload struct {
				Tool string `json:"tool"`
			}
			if json.Unmarshal(ev.Payload, &payload) == nil {
				tools = append(tools, payload.Tool)
			}
		case store.EventNotice:
			var payload struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(ev.Payload, &payload) == nil {
				notices = append(notices, payload.Code)
			}
		}
	}
	sess, err := engine.Session(ctx, sessionID)
	if err != nil {
		return "tools=" + strings.Join(tools, ",") + "; no session: " + err.Error()
	}
	var messages []struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	_ = json.Unmarshal(sess.Messages, &messages)
	last, lastTool, calls := "", "", ""
	for i := len(messages) - 1; i >= 0; i-- {
		if last == "" && messages[i].Role == "assistant" && strings.TrimSpace(messages[i].Content) != "" {
			last = messages[i].Content
		}
		if lastTool == "" && messages[i].Role == "tool" && strings.TrimSpace(messages[i].Content) != "" {
			lastTool = messages[i].Content
		}
		if calls == "" && len(messages[i].ToolCalls) > 0 {
			var parts []string
			for _, call := range messages[i].ToolCalls {
				parts = append(parts, call.Function.Name+"("+call.Function.Arguments+")")
			}
			calls = strings.Join(parts, ", ")
		}
	}
	if len(last) > 300 {
		last = last[:300]
	}
	if len(lastTool) > 500 {
		lastTool = lastTool[:500]
	}
	if len(calls) > 500 {
		calls = calls[:500]
	}
	return "tools=" + strings.Join(tools, ",") + "; calls=" + calls + "; notices=" + strings.Join(notices, ",") +
		"; lastTool=" + lastTool + "; last=" + last
}

// staticResponder is the attentive but unopinionated person the eval needs: skip the chips, answer the one choice
// the case scripts, and fill a gap with the case's text. It resolves each interaction through the session engine,
// so the run leaves the same events a real answer would.
type staticResponder struct {
	session *session.Engine
	store   *store.Store
	c       agent.Case
}

func (r *staticResponder) Wait(ctx context.Context, sessionID, interactionID string) (json.RawMessage, error) {
	question, err := r.store.Interaction(ctx, interactionID)
	if err != nil {
		return nil, err
	}
	var value any
	switch question.Kind {
	case "describe":
		value = map[string]any{"selected": map[string][]string{}}
	case "question_form":
		value = map[string]any{"skip_all": true}
	case "choose_medium":
		value = r.c.ChooseMedium
	case "choose_candidate":
		var payload struct {
			Candidates []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"candidates"`
		}
		_ = json.Unmarshal(question.Payload, &payload)
		for _, candidate := range payload.Candidates {
			if strings.EqualFold(candidate.Title, r.c.Title) {
				value = candidate.ID
				break
			}
		}
		if value == nil && len(payload.Candidates) > 0 {
			value = payload.Candidates[0].ID
		}
	case "revisit_choice":
		value = r.c.RevisitChoice
	case "lookup_failed":
		value = r.c.LookupChoice
	case "fill_gap":
		var payload struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(question.Payload, &payload)
		switch payload.Field {
		case "rating":
			value = r.c.FillRating
		case "body":
			value = r.c.FillBody
		}
	}
	if value == nil || value == "" {
		return nil, errors.New("the eval has no answer for " + question.Kind)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if _, err := r.session.Answer(ctx, sessionID, interactionID, json.RawMessage(raw), "eval-"+interactionID); err != nil {
		return nil, err
	}
	return raw, nil
}
