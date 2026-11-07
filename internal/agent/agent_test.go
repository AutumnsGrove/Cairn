package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AutumnsGrove/nova/proto"
	"github.com/AutumnsGrove/nova/provider"

	"github.com/AutumnsGrove/sepia/internal/banks"
	"github.com/AutumnsGrove/sepia/internal/decisions"
	"github.com/AutumnsGrove/sepia/internal/domain"
	"github.com/AutumnsGrove/sepia/internal/providers"
	"github.com/AutumnsGrove/sepia/internal/session"
	"github.com/AutumnsGrove/sepia/internal/store"
	"github.com/AutumnsGrove/sepia/internal/world"
)

// --- the rig ------------------------------------------------------------------------------------------------

const (
	olSearch = `{"docs": [
	  {"key": "/works/OL1W", "title": "The Lantern Keeper", "author_name": ["Ada Voss"],
	   "first_publish_year": 1999, "cover_i": 123, "edition_key": ["OL1M"]},
	  {"key": "/works/OL2W", "title": "Lantern Keepers at Sea", "author_name": ["Tomas Reed"],
	   "first_publish_year": 2004}
	]}`
	olWork = `{"key": "/works/OL1W", "title": "The Lantern Keeper", "first_publish_date": "1999",
	  "covers": [123], "subjects": ["Lighthouses", "Memory"],
	  "authors": [{"author": {"key": "/authors/OL9A"}}]}`
	olEditions = `{"entries": [
	  {"title": "The Lantern Keeper", "publish_date": "1999", "publishers": ["Northlight Press"],
	   "number_of_pages": 312, "isbn_13": ["9780000000001"], "series": ["Lantern"], "covers": [123]}
	]}`
	olAuthor = `{"name": "Ada Voss"}`

	tmdbSearch = `{"page": 1, "results": [
	  {"id": 101, "title": "The Lantern Keeper", "original_title": "The Lantern Keeper",
	   "release_date": "2019-03-08", "poster_path": "/lantern.jpg"}
	], "total_results": 1}`
	tmdbDetail = `{"id": 101, "imdb_id": "tt0000101",
	  "title": "The Lantern Keeper", "original_title": "The Lantern Keeper",
	  "release_date": "2019-03-08", "runtime": 118, "original_language": "en",
	  "poster_path": "/lantern.jpg", "genres": [{"id": 18, "name": "Drama"}],
	  "credits": {"cast": [{"name": "Ilse Marrow"}], "crew": [{"job": "Director", "name": "Ada Voss"}]},
	  "external_ids": {"imdb_id": "tt0000101"}}`
	tmdbTVSearch = `{"page": 1, "results": [
	  {"id": 202, "name": "The Quarry Line", "original_name": "The Quarry Line",
	   "first_air_date": "2016-06-02", "poster_path": "/quarry.jpg"}
	], "total_results": 1}`
	tmdbTVDetail = `{"id": 202, "name": "The Quarry Line", "original_name": "The Quarry Line",
	  "first_air_date": "2016-06-02", "number_of_seasons": 3, "number_of_episodes": 24,
	  "status": "Ended", "original_language": "en", "poster_path": "/quarry.jpg",
	  "genres": [{"id": 9648, "name": "Mystery"}],
	  "created_by": [{"name": "Nell Farrier"}],
	  "credits": {"cast": [{"name": "Ruth Okafor"}], "crew": []},
	  "external_ids": {"imdb_id": "tt0000202", "tvdb_id": 500202}}`
	tmdbConfig = `{"images": {"secure_base_url": "https://img.example/t/p/", "poster_sizes": ["w92", "w342", "original"]}}`
)

type rig struct {
	agent     *Agent
	store     *store.Store
	session   *session.Engine
	chat      *world.ChatServer
	tmdb      *world.Server
	ol        *world.Server
	fake      *world.Decisions
	decisions *decisions.PreReader
	finder    *providers.Finder
	responder *scriptedResponder
	clock     *world.FixedClock
}

type rigOptions struct {
	noResponder      bool
	noDecisions      bool
	modelOff         bool
	script           []world.ChatTurn
	chatFault        world.Fault
	turnDeadline     time.Duration
	maxInputTokens   int
	providerOverride provider.Provider
	// live records the events the protocol does not persist, so a test can assert on what a phone would show
	// while the turn runs.
	live func(Event)
}

func newRig(t *testing.T, opts rigOptions) *rig {
	t.Helper()
	ctx := context.Background()
	clock := world.NewFixedClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))

	db, err := store.Open(ctx, store.Options{
		Path: filepath.Join(t.TempDir(), "sepia.db"),
		Now:  clock.Now,
		NewID: func() string {
			return fmt.Sprintf("id-%d", time.Now().UnixNano())
		},
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if migrateErr := db.Migrate(ctx); migrateErr != nil {
		t.Fatalf("Migrate: %v", migrateErr)
	}
	engine := session.New(session.Config{Store: db, Now: clock.Now, ServerVersion: "test"})

	chat := world.StartChat("openrouter", clock)
	t.Cleanup(chat.Close)
	chat.Script(opts.script...)
	if opts.chatFault != "" {
		chat.SetFault("/v1/chat/completions", opts.chatFault)
	}

	tmdb := world.Start("tmdb", clock)
	t.Cleanup(tmdb.Close)
	tmdb.SetBody("/search/movie", []byte(tmdbSearch))
	tmdb.SetBody("/movie/101", []byte(tmdbDetail))
	tmdb.SetBody("/search/tv", []byte(tmdbTVSearch))
	tmdb.SetBody("/tv/202", []byte(tmdbTVDetail))
	tmdb.SetBody("/configuration", []byte(tmdbConfig))

	ol := world.Start("openlibrary", clock)
	t.Cleanup(ol.Close)
	ol.SetBody("/search.json", []byte(olSearch))
	ol.SetBody("/works/OL1W.json", []byte(olWork))
	ol.SetBody("/works/OL1W/editions.json", []byte(olEditions))
	ol.SetBody("/authors/OL9A.json", []byte(olAuthor))

	finder := providers.NewFinder(providers.Config{
		TMDBKey: "test-key", TMDBBaseURL: tmdb.URL(), TMDBCoverBase: tmdb.URL() + "/t/p/w92",
		OpenLibraryBaseURL: ol.URL(), OpenLibraryCoverBase: ol.URL() + "/covers",
		TMDBCallInterval: -1, OpenLibraryCallInterval: -1,
		DataDir: t.TempDir(), Clock: clock,
	})

	set, err := banks.LoadDir("../../config/questions")
	if err != nil {
		t.Fatalf("banks.LoadDir: %v", err)
	}
	fake := world.NewDecisions(clock)
	var pre *decisions.PreReader
	if !opts.noDecisions {
		reg, regErr := decisions.Load("../../config/decisions")
		if regErr != nil {
			t.Fatalf("decisions.Load: %v", regErr)
		}
		pre = decisions.New(decisions.Config{Registry: reg, Decider: fake})
	}

	responder := &scriptedResponder{session: engine, store: db}
	cfg := Config{
		Store: db, Session: engine, Finder: finder, Banks: set, Decisions: pre,
		Model: PinnedModel, BaseURL: chat.URL(), APIKey: "test-key",
		Now: clock.Now, TurnDeadline: opts.turnDeadline, MaxInputTokens: opts.maxInputTokens, Live: opts.live,
	}
	if opts.noResponder {
		cfg.Responder = nil
	} else {
		cfg.Responder = responder
	}
	if opts.modelOff {
		cfg.APIKey = ""
		cfg.Provider = nil
	}
	if opts.providerOverride != nil {
		cfg.Provider = opts.providerOverride
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return &rig{
		agent: a, store: db, session: engine, chat: chat, tmdb: tmdb, ol: ol,
		fake: fake, decisions: pre, finder: finder, responder: responder, clock: clock,
	}
}

// scriptDune is a confident book pre-read: log, a book, story and pacing already covered.
func scriptBook(fake *world.Decisions) {
	fake.Set("intent", map[string]float64{"log_review": 0.93, "chitchat": 0.02})
	fake.Set("medium", map[string]float64{"book": 0.88, "film": 0.05, "unknown": 0.02})
	fake.Set("format", map[string]float64{"print": 0.20, "not_stated": 0.70})
	fake.Set("revisit", map[string]float64{"no": 0.92, "yes": 0.04})
	fake.Set("mentions_story", map[string]float64{"yes": 0.90, "no": 0.10})
	fake.Set("mentions_pacing", map[string]float64{"yes": 0.88, "no": 0.12})
	fake.Set("extract_fidelity", map[string]float64{"faithful": 0.92, "drops_opinion": 0.05})
}

// scriptedResponder answers interactions in the order scripted. It resolves each one through the session engine, so
// the interaction_resolved event is written exactly as it will be in production.
type scriptedResponder struct {
	session *session.Engine
	store   *store.Store

	mu       sync.Mutex
	answers  []scriptedAnswer
	fillGaps map[string]string
	seen     []domain.Interaction
}

type scriptedAnswer struct {
	kind  string
	value any
}

func (r *scriptedResponder) script(kind string, value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers = append(r.answers, scriptedAnswer{kind: kind, value: value})
}

// scriptFillGap answers a fill_gap by the field it asks for, so a case missing both a rating and a body can
// supply each without depending on the order the tools ask.
func (r *scriptedResponder) scriptFillGap(field, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fillGaps == nil {
		r.fillGaps = make(map[string]string)
	}
	r.fillGaps[field] = value
}

func (r *scriptedResponder) Wait(ctx context.Context, sessionID, interactionID string) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	question, err := r.store.Interaction(ctx, interactionID)
	if err != nil {
		return nil, err
	}
	r.seen = append(r.seen, question)
	if question.Kind == "fill_gap" {
		var payload struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(question.Payload, &payload)
		if value, ok := r.fillGaps[payload.Field]; ok {
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			if _, err := r.session.Answer(ctx, sessionID, interactionID, json.RawMessage(raw), "resp-"+interactionID); err != nil {
				return nil, err
			}
			return raw, nil
		}
	}
	for i, answer := range r.answers {
		if answer.kind != question.Kind {
			continue
		}
		r.answers = append(r.answers[:i], r.answers[i+1:]...)
		raw, err := json.Marshal(answer.value)
		if err != nil {
			return nil, err
		}
		if _, err := r.session.Answer(ctx, sessionID, interactionID, json.RawMessage(raw), "resp-"+interactionID); err != nil {
			return nil, err
		}
		return raw, nil
	}
	return nil, fmt.Errorf("no scripted answer for %s", question.Kind)
}

// --- the embedding ------------------------------------------------------------------------------------------

func TestNewValidatesThePinnedModel(t *testing.T) {
	cases := map[string]string{
		"alias":     "deepseek/deepseek-v4.1-flash-latest",
		"tilde":     "~deepseek/deepseek-v4.1-flash-1017",
		"role":      "sepia_preread",
		"empty-ish": " ",
	}
	for name, model := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validatePin(model); err == nil {
				t.Fatalf("validatePin accepted model %q", model)
			}
		})
	}

	// The zero value names the pinned model, not an empty one.
	a := newRig(t, rigOptions{noResponder: true})
	if a.agent.Model() != PinnedModel {
		t.Errorf("model = %q", a.agent.Model())
	}
}

func TestDecodeArgsIsStrict(t *testing.T) {
	type args struct {
		Title string `json:"title"`
	}
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"clean", `{"title":"Dune"}`, true},
		{"unknown field", `{"title":"Dune","opinion":"loved it"}`, false},
		{"trailing data", `{"title":"Dune"}{"title":"Messiah"}`, false},
		{"not an object", `["Dune"]`, false},
		{"empty", ``, false},
		{"huge", `{"title":"` + strings.Repeat("a", maxToolArgs) + `"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got args
			err := decodeArgs(json.RawMessage(tc.raw), &got)
			if (err == nil) != tc.ok {
				t.Fatalf("decodeArgs(%q) = %v, want ok=%v", tc.name, err, tc.ok)
			}
		})
	}
}

func TestPromptsNeverInviteWriting(t *testing.T) {
	body := SystemPrompt()
	if body == "" {
		t.Fatal("the system prompt is empty")
	}
	for _, want := range []string{"Never write", "Her words are the only words", "no emojis"} {
		if !strings.Contains(body, want) {
			t.Errorf("the system prompt does not say %q", want)
		}
	}
	if hint := HintText(); !strings.Contains(hint, "not an instruction") || !strings.Contains(hint, "Automatic note") {
		t.Errorf("the hint is not framed as data:\n%s", hint)
	}
	for _, name := range []string{"media_lookup", "merge_candidates", "ask_reviewer", "save_entry", "search_library"} {
		if description := toolDescription(name); description == "" {
			t.Errorf("tool %s has no description file", name)
		}
	}
	// A prompt must never contain an invitation to compose her body or invent a quote. Prohibition words are
	// checked above; these are the positive instructions that would be a bug.
	forbidden := []string{"you may write", "write her", "make up", "invent a quote", "compose a body"}
	for _, name := range []string{"system", "hint", "tool_media_lookup", "tool_merge_candidates", "tool_ask_reviewer", "tool_save_entry", "tool_search_library"} {
		text := promptText(name)
		for _, phrase := range forbidden {
			if strings.Contains(strings.ToLower(text), phrase) {
				t.Errorf("prompt %s contains %q", name, phrase)
			}
		}
	}
}

func TestParseMessage(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		text   string
		rating int
		date   string
		format string
	}{
		{"clean", "finished it last night, 4.5/5, the middle dragged but the end stayed with me", 18, "2026-11-01", ""},
		{"vague", "a four and a bit, maybe", 0, "", ""},
		{"audiobook", "listened to the audiobook yesterday, 4 stars", 16, "2026-11-01", domain.FormatAudiobook},
		{"cinema", "saw it at the cinema on 30 October, 5/5", 20, "2026-10-30", domain.FormatCinema},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMessage(tc.text, now, "", "")
			if got.RatingQuarters() != tc.rating {
				t.Errorf("rating = %d, want %d", got.RatingQuarters(), tc.rating)
			}
			if got.DateString() != tc.date {
				t.Errorf("date = %q, want %q", got.DateString(), tc.date)
			}
			if got.FormatString() != tc.format {
				t.Errorf("format = %q, want %q", got.FormatString(), tc.format)
			}
		})
	}
}

func TestTranslatorMapsNovaEvents(t *testing.T) {
	r := newRig(t, rigOptions{noResponder: true})
	sess, err := r.session.CreateEntry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := &RunState{Agent: r.agent, SessionID: sess.ID}
	tr := &translator{ctx: context.Background(), agent: r.agent, state: state}
	tr.Emit(proto.Event{Type: proto.EventMessageStart})
	tr.Emit(proto.Event{Type: proto.EventTextDelta, Text: "hello"})
	tr.Emit(proto.Event{Type: proto.EventMessageEnd})
	tr.Emit(proto.Event{Type: proto.EventToolCall, Call: &proto.ToolCall{ID: "c1", Name: "media_lookup"}})
	tr.Emit(proto.Event{Type: proto.EventToolStart, CallID: "c1"})
	details, _ := json.Marshal(toolDetails{Summary: "found two"})
	tr.Emit(proto.Event{Type: proto.EventToolEnd, CallID: "c1", Result: &proto.ToolResult{Content: "ok", Details: details}})

	events, err := r.store.EventsSince(context.Background(), sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	want := "assistant_start,text_delta,assistant_end,tool_start,tool_end"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	var end struct {
		Tool    string `json:"tool"`
		OK      bool   `json:"ok"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(events[4].Payload, &end); err != nil {
		t.Fatal(err)
	}
	if end.Tool != "media_lookup" || !end.OK || end.Summary != "found two" {
		t.Errorf("tool_end = %+v", end)
	}
}

// The fake model's stream is real: the agent uses nova's OpenRouter provider against the world's chat fake.
func TestHandleMessageModelOffKeepsTheMessage(t *testing.T) {
	r := newRig(t, rigOptions{modelOff: true})
	scriptBook(r.fake)
	sess, err := r.session.CreateEntry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.agent.HandleMessage(context.Background(), TurnInput{
		SessionID: sess.ID, Text: "finished the lantern keeper last night, 4.5/5, loved it", ClientMsgID: "m1",
	})
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if out.Ended != "model_off" {
		t.Errorf("ended = %q", out.Ended)
	}
	messages, err := r.session.UserMessages(context.Background(), sess.ID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages = %v, %v", messages, err)
	}
	events, err := r.store.EventsSince(context.Background(), sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var notices int
	for _, ev := range events {
		if ev.Kind == store.EventNotice {
			notices++
		}
	}
	if notices == 0 {
		t.Error("no notice explained that the model is off")
	}
}
