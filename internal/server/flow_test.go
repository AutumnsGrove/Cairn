package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/cairn/internal/agent"
	"github.com/AutumnsGrove/cairn/internal/banks"
	"github.com/AutumnsGrove/cairn/internal/config"
	"github.com/AutumnsGrove/cairn/internal/decisions"
	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/providers"
	"github.com/AutumnsGrove/cairn/internal/session"
	"github.com/AutumnsGrove/cairn/internal/store"
	"github.com/AutumnsGrove/cairn/internal/world"
)

// The happy route from the slice plan, end to end: a scripted model, the real pipeline, a real socket, and her
// confirm. Nothing here is mocked except the model and the providers, which is exactly what the world is for.

func newFlowRig(t *testing.T) *socketRig {
	t.Helper()
	ctx := context.Background()
	clock := world.NewFixedClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))

	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "cairn.db"), Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.VerifyTriggers(ctx); err != nil {
		t.Fatal(err)
	}
	engine := session.New(session.Config{Store: db, Now: clock.Now, ServerVersion: "test"})

	chat := world.StartChat("openrouter", clock)
	t.Cleanup(chat.Close)
	chat.Script(world.SampleChatScript()...)
	tmdb := world.Start("tmdb", clock)
	t.Cleanup(tmdb.Close)
	tmdb.LoadBodies(world.SampleTMDB())
	ol := world.Start("openlibrary", clock)
	t.Cleanup(ol.Close)
	ol.LoadBodies(world.SampleOpenLibrary())

	reading := world.NewDecisions(clock)
	world.ScriptSampleDecisions(reading)
	registry, err := decisions.Load(filepath.Join("..", "..", "config", "decisions"))
	if err != nil {
		t.Fatal(err)
	}
	banksSet, err := banks.LoadDir(filepath.Join("..", "..", "config", "questions"))
	if err != nil {
		t.Fatal(err)
	}
	finder := providers.NewFinder(providers.Config{
		TMDBKey: "test-key", TMDBBaseURL: tmdb.URL(), TMDBCoverBase: tmdb.URL() + "/t/p/w92",
		OpenLibraryBaseURL: ol.URL(), OpenLibraryCoverBase: ol.URL() + "/covers",
		TMDBCallInterval: -1, OpenLibraryCallInterval: -1,
		DataDir: t.TempDir(), Clock: clock,
	})

	responder := NewResponder()
	live := NewLiveBus()
	model, err := agent.New(agent.Config{
		Store: db, Session: engine, Finder: finder, Banks: banksSet,
		Decisions: decisions.New(decisions.Config{Registry: registry, Decider: reading}),
		Model:     agent.PinnedModel, BaseURL: chat.URL(), APIKey: "test-key",
		Responder: responder, Live: live.Publish, Logger: slog.New(slog.DiscardHandler),
		Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host := ln.Addr().String()
	cfg := config.Config{AllowedHosts: []string{host, "localhost:8893"}, Addr: host, DataDir: t.TempDir()}
	srv := &httptest.Server{
		Listener: ln,
		Config: &http.Server{Handler: New(cfg, Deps{
			Engine: engine, Agent: model, Responder: responder, Live: live, Now: clock.Now,
		}, slog.New(slog.DiscardHandler), nil)},
	}
	srv.Start()
	return &socketRig{t: t, server: srv, db: db, engine: engine, host: host}
}

func TestScriptedSessionFromMessageToSaved(t *testing.T) {
	rig := newFlowRig(t)
	defer rig.close()
	ctx := context.Background()
	sess, err := rig.engine.CreateEntry(ctx)
	if err != nil {
		t.Fatal(err)
	}

	client := rig.dial(sess.ID, 0, "http://"+rig.host)
	client.expect("hello")
	client.sendJSON(map[string]any{
		"type": "message", "client_msg_id": "m1",
		"data": map[string]any{
			"text": world.SampleMessage, "client_date": "2026-11-01", "tz": "Europe/London",
		},
	})
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("message ack = %v", got)
	}

	// The turn streams, looks the work up, takes the candidate and builds the card.
	var version int
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		ev, ok := client.next()
		if !ok {
			t.Fatalf("socket closed during the turn (code %d), saw %v", ev.closeCode, seen)
		}
		seen[ev.Type] = true
		if ev.Type == "interaction_request" {
			// The card asks which work she meant. Answer it the way a person would, over the same socket.
			var data struct {
				InteractionID string `json:"interaction_id"`
				Kind          string `json:"kind"`
				Payload       struct {
					Candidates []struct {
						ID string `json:"id"`
					} `json:"candidates"`
				} `json:"payload"`
			}
			if err = json.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Kind != "choose_candidate" || len(data.Payload.Candidates) == 0 {
				t.Fatalf("unexpected interaction %s: %s", data.Kind, ev.Data)
			}
			client.sendJSON(map[string]any{
				"type": "interaction_response", "client_msg_id": "i1",
				"data": map[string]any{
					"interaction_id": data.InteractionID,
					"value":          map[string]any{"value": data.Payload.Candidates[0].ID},
				},
			})
			if got := client.expect("ack").field(t, "status"); got != "accepted" {
				t.Fatalf("interaction ack = %v", got)
			}
			continue
		}
		if ev.Type == "draft_snapshot" {
			var data struct {
				Version int `json:"version"`
				Draft   struct {
					Body   string `json:"body"`
					Rating struct {
						Quarters int `json:"quarters"`
					} `json:"rating"`
					Work struct {
						Title string `json:"title"`
					} `json:"work"`
					CanConfirm bool `json:"can_confirm"`
				} `json:"draft"`
			}
			if err = json.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			if data.Draft.CanConfirm {
				version = data.Version
				if data.Draft.Work.Title != "The Lantern Keeper" {
					t.Fatalf("work = %q", data.Draft.Work.Title)
				}
				if data.Draft.Rating.Quarters != 18 {
					t.Fatalf("rating = %d, want 18", data.Draft.Rating.Quarters)
				}
				if data.Draft.Body != "the ending landed but it was too long" {
					t.Fatalf("body = %q", data.Draft.Body)
				}
				break
			}
		}
	}
	if version == 0 {
		t.Fatalf("the card never became confirmable: %v", seen)
	}
	for _, want := range []string{"user_message", "assistant_start", "tool_start", "tool_end", "draft_snapshot"} {
		if !seen[want] {
			t.Errorf("the turn never sent %s", want)
		}
	}

	// Her confirm is the only commit.
	client.sendJSON(map[string]any{
		"type": "confirm", "client_msg_id": "c1", "data": map[string]any{"base_version": version},
	})
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("confirm ack = %v", got)
	}
	saved := client.expect("saved")
	entryID, _ := saved.field(t, "entry_id").(string)
	if entryID == "" {
		t.Fatal("saved carried no entry id")
	}

	library, _, err := rig.engine.Library(ctx, "", nil, domain.Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(library) != 1 || library[0].ID != entryID {
		t.Fatalf("library = %+v", library)
	}
	// "last night" is the night before the phone's date, which is what makes the date hers and not the potato's.
	if library[0].Body != "the ending landed but it was too long" ||
		!strings.Contains(library[0].FinishedOn, "2026-10-31") {
		t.Errorf("saved entry = %+v", library[0])
	}
}
