package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/AutumnsGrove/sepia/internal/domain"
)

// The socket's smaller actions: accepting what came from the web, putting a left-out passage back, closing a card,
// stopping a turn, and the two control frames a client sends by hand. Each test drives the real engine, so a refusal
// is the one the engine really makes.

// dialFresh connects at the session's current head, so nothing is replayed and a test sees only what happens after
// it connects. A test that reads a snapshot would otherwise race the replay and could assert on the old card.
func dialFresh(t *testing.T, rig *socketRig, sessionID string) *wsClient {
	t.Helper()
	head, err := rig.db.HeadSequence(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("HeadSequence: %v", err)
	}
	return rig.dial(sessionID, head, "http://"+rig.host)
}

// trustOf reads what her tap did to one source, named by the provider id the client saw.
func trustOf(t *testing.T, rig *socketRig, providerID string) domain.Trust {
	t.Helper()
	sources, err := rig.db.Sources(context.Background(), "w1")
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	for _, source := range sources {
		if source.ProviderID == providerID {
			return source.Trust
		}
	}
	t.Fatalf("no source %q on the work", providerID)
	return ""
}

// The card carries a web field that needs her tap. The accept names the field the way the client saw it, by provider
// and that provider's id, and the same tap twice changes one thing.
func TestSocketAcceptanceNamesFieldsAndHappensOnce(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	ctx := context.Background()
	sess := rig.newDraft()
	web := func(providerID string) domain.Source {
		return domain.Source{Provider: "web", ProviderID: providerID, Trust: domain.TrustUnverified}
	}
	for _, source := range []domain.Source{web("https://example.test/dune"), web("https://example.test/dune-2")} {
		if _, err := rig.db.AddSource(ctx, "w1", source); err != nil {
			t.Fatalf("AddSource: %v", err)
		}
	}
	client := dialFresh(t, rig, sess.ID)
	client.expect("hello")

	client.sendJSON(map[string]any{
		"type": "accept_unverified", "client_msg_id": "a1",
		"data": map[string]any{"base_version": 0, "fields": []string{"web|https://example.test/dune"}},
	})
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("accept ack = %v", got)
	}
	client.expect("draft_snapshot")
	if trust := trustOf(t, rig, "https://example.test/dune"); trust != domain.TrustAccepted {
		t.Fatalf("the tapped field is %s, want accepted", trust)
	}
	if trust := trustOf(t, rig, "https://example.test/dune-2"); trust != domain.TrustUnverified {
		t.Fatalf("the untapped field is %s, want unverified", trust)
	}

	// The same client message is a duplicate, not a second tap.
	client.sendJSON(map[string]any{
		"type": "accept_unverified", "client_msg_id": "a1",
		"data": map[string]any{"base_version": 1, "fields": []string{"web|https://example.test/dune"}},
	})
	if got := client.expect("ack").field(t, "status"); got != "duplicate" {
		t.Fatalf("repeat accept ack = %v, want duplicate", got)
	}

	// "Accept all" is the same tap for every unverified field left on the work.
	client.sendJSON(map[string]any{
		"type": "accept_unverified", "client_msg_id": "a2",
		"data": map[string]any{"base_version": 1, "all": true},
	})
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("accept-all ack = %v", got)
	}
	client.expect("draft_snapshot")
	if trust := trustOf(t, rig, "https://example.test/dune-2"); trust != domain.TrustAccepted {
		t.Errorf("the second field is %s after accept-all, want accepted", trust)
	}

	// A field that is not on this work, and a card that has nothing left to accept, are both refusals with a code.
	client.sendJSON(map[string]any{
		"type": "accept_unverified", "client_msg_id": "a3",
		"data": map[string]any{"base_version": 2, "fields": []string{"web|https://example.test/not-here"}},
	})
	if ack := client.expect("ack"); ack.field(t, "status") != "rejected" || ack.field(t, "reason") != "invalid_value" {
		t.Errorf("unknown field ack = %+v", ack)
	}
	client.sendJSON(map[string]any{
		"type": "accept_unverified", "client_msg_id": "a4",
		"data": map[string]any{"base_version": 2, "all": true},
	})
	if ack := client.expect("ack"); ack.field(t, "status") != "rejected" || ack.field(t, "reason") != "invalid_value" {
		t.Errorf("accept-all with nothing left ack = %+v", ack)
	}
}

// A left-out passage is one of her own, so the socket accepts it only when it is really in what she wrote. The
// segment travels as a pair of offsets, never as text the client could invent.
func TestSocketPutsALeftOutPassageBack(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	ctx := context.Background()
	const message = "the lantern keeper was slow to start, but the ending landed"
	sess, err := rig.engine.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if _, err := rig.engine.AppendUserMessage(ctx, sess.ID, message, "m1"); err != nil {
		t.Fatalf("AppendUserMessage: %v", err)
	}
	// The extract kept the second half; the first half is the left-out segment she taps to put back.
	start := len("the lantern keeper was slow to start, but ")
	if _, err := rig.engine.StartDraft(ctx, sess.ID, domain.Entry{
		WorkID: "w1", Body: "the ending landed", BodyOrigin: domain.BodyExtracted,
		BodySpans: []domain.Span{{Start: start, End: len(message), Text: "the ending landed"}},
	}); err != nil {
		t.Fatalf("StartDraft: %v", err)
	}

	client := dialFresh(t, rig, sess.ID)
	client.expect("hello")
	client.sendJSON(map[string]any{
		"type": "include_left_out", "client_msg_id": "i1",
		"data": map[string]any{"base_version": 0, "segment_id": "seg_0_41"},
	})
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("include ack = %v", got)
	}
	snapshot := client.expect("draft_snapshot")
	var data struct {
		Version int `json:"version"`
		Draft   struct {
			Body string `json:"body"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(snapshot.Data, &data); err != nil {
		t.Fatal(err)
	}
	// Each span is one line of the body, so the passage she put back sits above the one the extract kept.
	if data.Draft.Body != "the lantern keeper was slow to start, but\nthe ending landed" {
		t.Fatalf("body after the tap = %q", data.Draft.Body)
	}

	// A segment past the end of what she wrote, and one that is not a segment at all, are refusals.
	for _, segment := range []string{"seg_500_600", "not-a-segment", "seg_9_9"} {
		client.sendJSON(map[string]any{
			"type": "include_left_out", "client_msg_id": "i-" + segment,
			"data": map[string]any{"base_version": data.Version, "segment_id": segment},
		})
		if ack := client.expect("ack"); ack.field(t, "status") != "rejected" {
			t.Errorf("segment %q ack = %+v, want a rejection", segment, ack)
		}
	}
}

// A typed body has no extract to add to, so the same message is refused with the engine's stable code rather than
// with an internal failure. The segment is in range, so the refusal comes from the engine and not the offsets.
func TestSocketRefusesAPassageForATypedBody(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	ctx := context.Background()
	sess, err := rig.engine.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if _, err := rig.engine.AppendUserMessage(ctx, sess.ID, "it was slow, and then it was not", "m1"); err != nil {
		t.Fatalf("AppendUserMessage: %v", err)
	}
	if _, err := rig.engine.StartDraft(ctx, sess.ID, domain.Entry{WorkID: "w1", Body: "it was slow"}); err != nil {
		t.Fatalf("StartDraft: %v", err)
	}
	client := dialFresh(t, rig, sess.ID)
	client.expect("hello")
	client.sendJSON(map[string]any{
		"type": "include_left_out", "client_msg_id": "i1",
		"data": map[string]any{"base_version": 0, "segment_id": "seg_0_4"},
	})
	ack := client.expect("ack")
	if ack.field(t, "status") != "rejected" || ack.field(t, "reason") != "not_extracted" {
		t.Fatalf("ack = %+v, want a not_extracted refusal", ack)
	}
}

// Discard closes the card once. A second tap under the same client message is a duplicate, and a fresh one on a
// closed session is refused with the session's own code.
func TestSocketDiscardsOnceAndThenIsClosed(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	ctx := context.Background()
	sess := rig.newDraft()
	client := dialFresh(t, rig, sess.ID)
	client.expect("hello")

	client.sendJSON(map[string]any{"type": "discard", "client_msg_id": "d1", "data": map[string]any{}})
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("discard ack = %v", got)
	}
	client.sendJSON(map[string]any{"type": "discard", "client_msg_id": "d1", "data": map[string]any{}})
	if got := client.expect("ack").field(t, "status"); got != "duplicate" {
		t.Fatalf("repeat discard ack = %v, want duplicate", got)
	}
	client.sendJSON(map[string]any{"type": "discard", "client_msg_id": "d2", "data": map[string]any{}})
	ack := client.expect("ack")
	if ack.field(t, "status") != "rejected" || ack.field(t, "reason") != "session_closed" {
		t.Fatalf("discard after closing = %+v", ack)
	}

	stored, err := rig.db.Session(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if stored.State != domain.SessionDiscarded {
		t.Errorf("session state = %q", stored.State)
	}
}

// A message with a screenshot attached is refused rather than quietly dropped, because images are a later slice and
// losing something she meant to send is worse than saying no.
func TestSocketRefusesAttachmentsAndStopAndPing(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()
	client := dialFresh(t, rig, sess.ID)
	client.expect("hello")

	client.sendJSON(map[string]any{
		"type": "message", "client_msg_id": "m1",
		"data": map[string]any{
			"text": "a review", "attachments": []string{"shot-1"},
			"client_date": "2026-11-01", "tz": "Europe/London",
		},
	})
	if ack := client.expect("ack"); ack.field(t, "reason") != "not_implemented" {
		t.Fatalf("attachment ack = %+v", ack)
	}

	// stop cancels a turn that is not running: it is a no-op for the hub, and still answered.
	client.sendJSON(map[string]any{"type": "stop", "client_msg_id": "s1", "data": map[string]any{}})
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("stop ack = %v", got)
	}
}

// A ping frame is answered with a pong frame carrying the same payload, which is what a phone on a bad connection
// uses to tell a quiet socket from a dead one.
func TestSocketAnswersAPingFrame(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	sess := rig.newDraft()
	client := dialFresh(t, rig, sess.ID)
	client.expect("hello")

	payload := []byte("are you there")
	client.send(opPing, payload)
	// The pong may arrive after a replayed event, so read until it does.
	for i := 0; i < 20; i++ {
		_ = client.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		f, err := readFrameServer(client.br)
		if err != nil {
			t.Fatalf("read pong: %v", err)
		}
		if f.opcode != opPong {
			continue
		}
		if string(f.payload) != string(payload) {
			t.Errorf("pong payload = %q, want %q", f.payload, payload)
		}
		return
	}
	t.Fatal("the socket never answered the ping")
}

// The card's work marker is decided by the work and its sources together: verified is best, every field accepted is
// next, and anything still unverified says so. The client redraws the marker straight from the snapshot, so it is
// asserted on the wire.
func TestCardWorkMarkerFollowsTheSources(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	ctx := context.Background()
	if err := rig.db.SaveWork(ctx, domain.Work{ID: "w2", Medium: "book", Title: "The Quarry Line"}); err != nil {
		t.Fatalf("SaveWork: %v", err)
	}
	if _, err := rig.db.AddSource(ctx, "w2", domain.Source{
		Provider: "web", ProviderID: "https://example.test/quarry", Trust: domain.TrustAccepted,
	}); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	sess, err := rig.engine.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if _, err := rig.engine.StartDraft(ctx, sess.ID, domain.Entry{WorkID: "w2"}); err != nil {
		t.Fatalf("StartDraft: %v", err)
	}
	if got := cardWorkMarker(t, rig, sess.ID); got != "accepted" {
		t.Fatalf("marker with every field accepted = %q, want accepted", got)
	}

	// One field that still needs a tap moves the marker back.
	if _, err := rig.db.AddSource(ctx, "w2", domain.Source{
		Provider: "web", ProviderID: "https://example.test/quarry-2", Trust: domain.TrustUnverified,
	}); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	if got := cardWorkMarker(t, rig, sess.ID); got != "unverified" {
		t.Fatalf("marker with an unverified field = %q, want unverified", got)
	}
}

// cardWorkMarker dials the session and reads the work block of the card hello carries.
func cardWorkMarker(t *testing.T, rig *socketRig, sessionID string) string {
	t.Helper()
	client := dialFresh(t, rig, sessionID)
	var data struct {
		Draft struct {
			Blocks struct {
				Work string `json:"work"`
			} `json:"blocks"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(client.expect("hello").Data, &data); err != nil {
		t.Fatal(err)
	}
	return data.Draft.Blocks.Work
}

// The save guard runs before anything is written, so a body made of words she never wrote is refused with its own
// code and the card stays a draft.
func TestSocketRefusesABodyThatIsNotHerWords(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	ctx := context.Background()
	sess, err := rig.engine.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if _, appendErr := rig.engine.AppendUserMessage(ctx, sess.ID, "the lantern keeper was slow to start", "m1"); appendErr != nil {
		t.Fatalf("AppendUserMessage: %v", appendErr)
	}
	// The card claims an extracted body in a sentence that is nowhere in her message.
	if _, startErr := rig.engine.StartDraft(ctx, sess.ID, domain.Entry{
		WorkID: "w1", Rating: 18, Body: "a line the model wrote", BodyOrigin: domain.BodyExtracted,
	}); startErr != nil {
		t.Fatalf("StartDraft: %v", startErr)
	}
	client := dialFresh(t, rig, sess.ID)
	client.expect("hello")
	client.sendJSON(map[string]any{
		"type": "confirm", "client_msg_id": "c1", "data": map[string]any{"base_version": 0},
	})
	ack := client.expect("ack")
	if ack.field(t, "status") != "rejected" || ack.field(t, "reason") != domain.CodeBodyNotYourWords {
		t.Fatalf("refusal = %+v, want %s", ack, domain.CodeBodyNotYourWords)
	}
	draft, err := rig.engine.Draft(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if draft.Entry.Status != domain.StatusDraft {
		t.Errorf("the card was saved: %+v", draft.Entry)
	}
}

// An answer repeated under the same client message is a duplicate, not a second resolution: the first tap wins and
// the retry is told so.
func TestSocketAnswersAnInteractionOnce(t *testing.T) {
	rig := newSocketRig(t)
	defer rig.close()
	ctx := context.Background()
	sess, err := rig.engine.CreateEntry(ctx)
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	question, err := rig.engine.Ask(ctx, sess.ID, "question_form", map[string]any{"questions": []any{}})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	client := dialFresh(t, rig, sess.ID)
	client.expect("hello")
	answer := map[string]any{
		"type": "interaction_response", "client_msg_id": "i1",
		"data": map[string]any{"interaction_id": question.ID, "value": map[string]any{"quarters": 18}},
	}
	client.sendJSON(answer)
	if got := client.expect("ack").field(t, "status"); got != "accepted" {
		t.Fatalf("answer ack = %v", got)
	}
	client.sendJSON(answer)
	if got := client.expect("ack").field(t, "status"); got != "duplicate" {
		t.Fatalf("repeat answer ack = %v, want duplicate", got)
	}
}
