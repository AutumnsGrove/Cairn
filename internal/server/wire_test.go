package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/AutumnsGrove/sepia/internal/contract"
	"github.com/AutumnsGrove/sepia/internal/domain"
	"github.com/AutumnsGrove/sepia/internal/session"
	"github.com/AutumnsGrove/sepia/internal/store"
)

// The contract test for the wire layer: every event this package can produce is validated against the same schema
// the frontend uses, and every REST body against the same OpenAPI document it generates its types from. A field
// added here and not there fails this, not a browser.

func newSessionRig(t *testing.T) (*Server, *store.Store, *session.Engine, domain.Session) {
	t.Helper()
	ctx := context.Background()
	clock := func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "sepia.db"), Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.VerifyTriggers(ctx); err != nil {
		t.Fatal(err)
	}
	engine := session.New(session.Config{Store: db, Now: clock, ServerVersion: "test"})
	s := newServer(testConfig(), Deps{Engine: engine, Now: clock}, slog.New(slog.DiscardHandler), nil)
	if err = db.SaveWork(ctx, domain.Work{
		ID: "w1", Medium: "book", Title: "The Lantern Keeper", Year: 1999, Verified: true,
		PrimaryProvider: "openlibrary", PrimaryID: "OL1W",
		Creators: []domain.Creator{{Role: "author", Name: "Ada Voss"}},
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := engine.CreateEntry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.StartDraft(ctx, sess.ID, domain.Entry{WorkID: "w1"}); err != nil {
		t.Fatal(err)
	}
	return s, db, engine, sess
}

func validateEvent(t *testing.T, raw []byte) {
	t.Helper()
	validator, err := contract.Default()
	if err != nil {
		t.Fatalf("contract: %v", err)
	}
	if err := validator.ValidateServerEvent(raw); err != nil {
		t.Fatalf("event is not in the contract: %v\n%s", err, raw)
	}
}

func TestHelloAndSnapshotsValidateAgainstTheEventSchema(t *testing.T) {
	ctx := context.Background()
	s, _, engine, sess := newSessionRig(t)
	message := "finished the lantern keeper last night, 4.5, the ending landed but it was too long"
	if _, err := engine.AppendUserMessage(ctx, sess.ID, message, "m1"); err != nil {
		t.Fatal(err)
	}
	draft, err := engine.SetExtractedBody(ctx, sess.ID, 0, message,
		[]string{"the ending landed but it was too long"}, "the ending landed", "m2")
	if err != nil {
		t.Fatal(err)
	}
	if draft.Entry.Rating == 0 {
		if _, err = engine.EditDraft(ctx, sess.ID, session.Edit{
			BaseVersion: draft.Version,
			Set:         map[string]json.RawMessage{"rating": json.RawMessage("18")}, ClientMsgID: "m3",
		}); err != nil {
			t.Fatal(err)
		}
	}

	greeted, err := engine.Greet(ctx, sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	hello, err := s.hello(ctx, greeted)
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	raw, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	validateEvent(t, raw)

	// Every persisted event, including the draft snapshot the socket replays, is validated as it would be sent.
	events, err := engine.EventsSince(ctx, sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawSnapshot bool
	for _, ev := range events {
		wire, wireErr := s.wireEvent(ctx, ev)
		if wireErr != nil {
			t.Fatalf("%s: %v", ev.Kind, wireErr)
		}
		validateEvent(t, wire)
		if ev.Kind == "draft_snapshot" {
			sawSnapshot = true
		}
	}
	if !sawSnapshot {
		t.Fatal("no draft_snapshot was written")
	}
}

func TestTheCardCarriesWhatTheEntryCardSpecSays(t *testing.T) {
	ctx := context.Background()
	_, _, engine, sess := newSessionRig(t)
	message := "hey so i finished the lantern keeper last night, the ending landed but it was too long"
	if _, err := engine.AppendUserMessage(ctx, sess.ID, message, "m1"); err != nil {
		t.Fatal(err)
	}
	draft, err := engine.SetExtractedBody(ctx, sess.ID, 0, message,
		[]string{"the ending landed but it was too long"}, "the ending landed", "m2")
	if err != nil {
		t.Fatal(err)
	}
	draft, err = engine.EditDraft(ctx, sess.ID, session.Edit{
		BaseVersion: draft.Version,
		Set:         map[string]json.RawMessage{"rating": json.RawMessage("18")}, ClientMsgID: "m3",
	})
	if err != nil {
		t.Fatal(err)
	}
	card, err := engine.Card(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	wire := newServer(testConfig(), Deps{}, slog.New(slog.DiscardHandler), nil).wireCardOf(card)
	if wire.Work.Title != "The Lantern Keeper" || wire.Rating.Quarters != 18 {
		t.Errorf("card = %+v", wire)
	}
	if wire.Blocks.Work != "verified" || wire.Blocks.Rating != "set" || wire.Blocks.Body != "extracted" {
		t.Errorf("blocks = %+v", wire.Blocks)
	}
	if !wire.CanConfirm || len(wire.Missing) != 0 {
		t.Errorf("can_confirm = %v, missing = %v", wire.CanConfirm, wire.Missing)
	}
	if len(wire.LeftOut) == 0 || len(wire.BodySpans) != 1 {
		t.Errorf("left out = %d, spans = %d", len(wire.LeftOut), len(wire.BodySpans))
	}
	if len(wire.Answers) != 0 {
		t.Errorf("answers = %+v", wire.Answers)
	}
}

// --- OpenAPI REST shapes ---------------------------------------------------------------------------------

// openAPISchemas compiles the component schemas from api/openapi.yaml. OpenAPI 3.1 components are JSON Schema, so
// the same validator runs both documents.
func openAPISchemas(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml: %v", err)
	}
	root := map[string]any{
		"$schema":    "https://json-schema.org/draft/2020-12/schema",
		"components": map[string]any{"schemas": doc.Components.Schemas},
		"$ref":       "#/components/schemas/Health",
	}
	body, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	docValue, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("https://sepia.invalid/openapi.json", docValue); err != nil {
		t.Fatal(err)
	}
	return compiler
}

func validateSchema(t *testing.T, compiler *jsonschema.Compiler, name string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://sepia.invalid/openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("%s does not match the OpenAPI schema: %v\n%s", name, err, body)
	}
}

func TestRESTShapesValidateAgainstTheOpenAPIDocument(t *testing.T) {
	ctx := context.Background()
	_, _, engine, sess := newSessionRig(t)
	compiler := openAPISchemas(t)

	// A session page and one session.
	sessions, err := engine.Sessions(ctx, domain.SessionActive, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	validateSchema(t, compiler, "Session", wireSessionOf(sessions[0], string(session.WaitingPickWork)))

	// Confirm an entry, then validate the entry, work and revision bodies.
	if _, err = engine.EditDraft(ctx, sess.ID, session.Edit{BaseVersion: 0, Set: map[string]json.RawMessage{
		"rating": json.RawMessage("18"), "body": json.RawMessage(`"A quiet book about light."`),
		"finished_on": json.RawMessage(`"2026-10-30"`),
	}, ClientMsgID: "m1"}); err != nil {
		t.Fatal(err)
	}
	entry, _, _, err := engine.Confirm(ctx, sess.ID, session.ConfirmRequest{BaseVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	view, err := engine.EntryView(ctx, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	validateSchema(t, compiler, "Entry", wireEntryOf(view))
	workView, err := engine.WorkView(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	validateSchema(t, compiler, "WorkDetail", wireWorkDetailOf(workView))
	validateSchema(t, compiler, "Work", wireWorkOf(workView.Work))

	revisions, _, err := engine.RevisionsPage(ctx, entry.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 0 {
		t.Fatal("a first confirm wrote a revision; there was nothing to replace")
	}
	// The revision body is the same card with a number, so one synthetic revision proves the shape.
	sample := domain.Revision{
		ID: "rev1", EntryID: entry.ID, Number: 1,
		Snapshot: entry, CreatedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
	}
	validateSchema(t, compiler, "Revision", wireRevisionOf(sample, workView.Work, nil))

	// The static replies the UI reads.
	validateSchema(t, compiler, "VersionInfo", map[string]any{
		"version": "test", "deployment": "bare",
		"updater": map[string]any{"state": "not_installed", "heartbeat_at": nil},
	})
}

// A body that is only framing leaves left-out text and an empty body, which is the card's honest state.
func TestWireCardLeavesOutTheFraming(t *testing.T) {
	ctx := context.Background()
	_, _, engine, sess := newSessionRig(t)
	message := "hey so i finished the lantern keeper last night"
	if _, err := engine.AppendUserMessage(ctx, sess.ID, message, "m1"); err != nil {
		t.Fatal(err)
	}
	card, err := engine.Card(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	wire := newServer(testConfig(), Deps{}, slog.New(slog.DiscardHandler), nil).wireCardOf(card)
	if wire.Blocks.Body != "empty" {
		t.Errorf("body block = %q", wire.Blocks.Body)
	}
	if wire.CanConfirm {
		t.Error("a card with no rating or body said it could confirm")
	}
	if len(wire.Missing) == 0 {
		t.Error("the card did not say what it is missing")
	}
	if len(wire.LeftOut) != 1 || wire.LeftOut[0].Text != message {
		t.Errorf("left out = %+v", wire.LeftOut)
	}
}
