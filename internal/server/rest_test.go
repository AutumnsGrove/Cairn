package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AutumnsGrove/cairn/internal/domain"
	"github.com/AutumnsGrove/cairn/internal/session"
	"github.com/AutumnsGrove/cairn/internal/store"
)

// The REST half: every route on the happy path, every refusal, and the two sad shapes a page can produce (a gone
// session and a database row that is not there).

func newREST(t *testing.T) (http.Handler, *session.Engine, *store.Store) {
	t.Helper()
	// newSocketRig already has a real store, engine and work; REST shares them.
	rig := newSocketRig(t)
	return rig.server.Config.Handler, rig.engine, rig.db
}

func doJSON(t *testing.T, h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "localhost:8893"
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", "http://localhost:8893")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSessionsCreateAndList(t *testing.T) {
	h, _, _ := newREST(t)

	rec := doJSON(t, h, http.MethodPost, "/api/sessions", map[string]any{"kind": "entry"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	var created wireSession
	decode(t, rec, &created)
	if created.Kind != "entry" || created.State != "active" || created.WaitingReason == nil ||
		*created.WaitingReason != "pick_work" {
		t.Errorf("session = %+v", created)
	}

	general := doJSON(t, h, http.MethodPost, "/api/sessions", map[string]any{"kind": "general", "title": "next reads"})
	if general.Code != http.StatusCreated {
		t.Fatalf("general = %d: %s", general.Code, general.Body)
	}

	list := doJSON(t, h, http.MethodGet, "/api/sessions?state=active&limit=1", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", list.Code, list.Body)
	}
	var page struct {
		Items      []wireSession `json:"items"`
		NextCursor *string       `json:"next_cursor"`
	}
	decode(t, list, &page)
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("page = %+v", page)
	}
	next := doJSON(t, h, http.MethodGet, "/api/sessions?state=active&limit=1&cursor="+*page.NextCursor, nil)
	var second struct {
		Items []wireSession `json:"items"`
	}
	decode(t, next, &second)
	if len(second.Items) != 1 || second.Items[0].ID == page.Items[0].ID {
		t.Errorf("second page = %+v", second.Items)
	}

	one := doJSON(t, h, http.MethodGet, "/api/sessions/"+created.ID, nil)
	if one.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", one.Code, one.Body)
	}
	if missing := doJSON(t, h, http.MethodGet, "/api/sessions/no-such", nil); missing.Code != http.StatusNotFound {
		t.Errorf("missing session = %d", missing.Code)
	}

	// A general chat has no draft, so no waiting reason.
	var chat wireSession
	decode(t, general, &chat)
	if chat.WaitingReason != nil {
		t.Errorf("a chat has a waiting reason: %v", *chat.WaitingReason)
	}
}

func TestSessionsRefuseBadRequests(t *testing.T) {
	h, _, _ := newREST(t)

	if rec := doJSON(t, h, http.MethodPost, "/api/sessions", map[string]any{"kind": "nonsense"}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown kind = %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/sessions", map[string]any{"kind": "entry", "surprise": 1}); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field = %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/sessions", strings.Repeat("x", maxJSONBody+1)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge body = %d", rec.Code)
	}

	// A body sent as a form is refused before it is parsed: a cross-site form can send one.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/sessions",
		strings.NewReader(`{"kind":"entry"}`))
	req.Host = "localhost:8893"
	req.Header.Set("Origin", "http://localhost:8893")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("wrong content type = %d", rec.Code)
	}

	if rec := doJSON(t, h, http.MethodGet, "/api/sessions?state=nonsense", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown state = %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/sessions?limit=zero", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad limit = %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/sessions?cursor=not!base64", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad cursor = %d", rec.Code)
	}
}

// aConfirmedEntry makes and confirms one saved entry with a finished date, so the library order is deterministic.
func aConfirmedEntry(t *testing.T, engine *session.Engine, db *store.Store, title, finished string) domain.Entry {
	t.Helper()
	ctx := context.Background()
	if err := db.SaveWork(ctx, domain.Work{
		ID: workIDFor(title), Medium: "book", Title: title, Verified: true,
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := engine.CreateEntry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.StartDraft(ctx, sess.ID, domain.Entry{WorkID: workIDFor(title)}); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.EditDraft(ctx, sess.ID, session.Edit{BaseVersion: 0, Set: map[string]json.RawMessage{
		"rating":      json.RawMessage("18"),
		"body":        json.RawMessage(`"A quiet book about light and memory."`),
		"finished_on": json.RawMessage(`"` + finished + `"`),
	}, ClientMsgID: "e-" + title}); err != nil {
		t.Fatal(err)
	}
	entry, _, _, err := engine.Confirm(ctx, sess.ID, session.ConfirmRequest{BaseVersion: 1, Revises: ""})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func workIDFor(title string) string {
	id := strings.ToLower(strings.ReplaceAll(title, " ", "-"))
	return "w-" + id
}

func TestLibraryListAndPagination(t *testing.T) {
	h, engine, db := newREST(t)

	empty := doJSON(t, h, http.MethodGet, "/api/entries", nil)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty library = %d: %s", empty.Code, empty.Body)
	}
	var emptyPage struct {
		Items      []wireEntry `json:"items"`
		NextCursor *string     `json:"next_cursor"`
	}
	decode(t, empty, &emptyPage)
	if len(emptyPage.Items) != 0 || emptyPage.NextCursor != nil {
		t.Errorf("empty page = %+v", emptyPage)
	}

	first := aConfirmedEntry(t, engine, db, "The Lantern Keeper", "2026-10-30")
	aConfirmedEntry(t, engine, db, "The Quarry Line", "2026-10-31")

	page := doJSON(t, h, http.MethodGet, "/api/entries?limit=1", nil)
	var got struct {
		Items      []wireEntry `json:"items"`
		NextCursor *string     `json:"next_cursor"`
	}
	decode(t, page, &got)
	if len(got.Items) != 1 || got.Items[0].FinishedOn != "2026-10-31" || got.NextCursor == nil {
		t.Fatalf("page = %+v", got)
	}
	nextPage := doJSON(t, h, http.MethodGet, "/api/entries?limit=1&cursor="+*got.NextCursor, nil)
	var second struct {
		Items      []wireEntry `json:"items"`
		NextCursor *string     `json:"next_cursor"`
	}
	decode(t, nextPage, &second)
	if len(second.Items) != 1 || second.Items[0].FinishedOn != "2026-10-30" || second.NextCursor != nil {
		t.Fatalf("second page = %+v", second)
	}

	one := doJSON(t, h, http.MethodGet, "/api/entries/"+first.ID, nil)
	if one.Code != http.StatusOK {
		t.Fatalf("entry = %d: %s", one.Code, one.Body)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/entries?medium=nonsense", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad medium = %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/entries?favourite=maybe", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad favourite = %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/entries/no-such", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing entry = %d", rec.Code)
	}
}

func TestEntryRevisionsAndDelete(t *testing.T) {
	h, engine, db := newREST(t)
	entry := aConfirmedEntry(t, engine, db, "The Lantern Keeper", "2026-10-30")

	revs := doJSON(t, h, http.MethodGet, "/api/entries/"+entry.ID+"/revisions", nil)
	if revs.Code != http.StatusOK {
		t.Fatalf("revisions = %d: %s", revs.Code, revs.Body)
	}
	var page struct {
		Items []wireRevision `json:"items"`
	}
	decode(t, revs, &page)
	if len(page.Items) != 0 {
		t.Errorf("a first confirm has revisions: %+v", page.Items)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/entries/no-such/revisions", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing revisions = %d", rec.Code)
	}

	del := doJSON(t, h, http.MethodPost, "/api/entries/"+entry.ID+"/delete", nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", del.Code, del.Body)
	}
	after := doJSON(t, h, http.MethodGet, "/api/entries", nil)
	var listed struct {
		Items []wireEntry `json:"items"`
	}
	decode(t, after, &listed)
	if len(listed.Items) != 0 {
		t.Errorf("a deleted entry is still in the library: %+v", listed.Items)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/entries/no-such/delete", nil); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing = %d", rec.Code)
	}
}

func TestWorksSearchAndTheNotBuiltStubs(t *testing.T) {
	h, _, _ := newREST(t)
	if rec := doJSON(t, h, http.MethodGet, "/api/works/w1", nil); rec.Code != http.StatusOK {
		t.Errorf("work = %d: %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/works/no-such", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing work = %d", rec.Code)
	}

	if rec := doJSON(t, h, http.MethodGet, "/api/search?q=lantern", nil); rec.Code != http.StatusNotImplemented {
		t.Errorf("search = %d, want not built", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/search?q=", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("empty search = %d", rec.Code)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/export"},
		{http.MethodPost, "/api/update/check"},
		{http.MethodGet, "/api/update/available"},
		{http.MethodPost, "/api/update/start"},
		{http.MethodGet, "/api/update/status"},
		{http.MethodPost, "/api/restart"},
	} {
		if rec := doJSON(t, h, route.method, route.path, nil); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want not built", route.method, route.path, rec.Code)
		}
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/version", nil); rec.Code != http.StatusOK {
		t.Errorf("version = %d: %s", rec.Code, rec.Body)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/services", nil); rec.Code != http.StatusOK {
		t.Errorf("services = %d: %s", rec.Code, rec.Body)
	}
	// An unknown API path and a wrong method both answer the envelope.
	if rec := doJSON(t, h, http.MethodGet, "/api/nonesuch", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown api path = %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/api/entries", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method = %d", rec.Code)
	}
}

func TestCoverIsServedWithoutTraversal(t *testing.T) {
	h, _, _ := newREST(t)
	// The rig's data directory is the server's; find it through a known file we write ourselves.
	cfg := testConfig()
	dir := t.TempDir()
	cfg.DataDir = dir
	if err := os.MkdirAll(filepath.Join(dir, "covers"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "covers", "lantern.jpg"), []byte("JPEG-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := New(cfg, Deps{}, slog.New(slog.DiscardHandler), nil)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/covers/lantern.jpg", nil)
	req.Host = "localhost:8893"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "JPEG-BYTES" {
		t.Fatalf("cover = %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content type = %q", ct)
	}

	for _, path := range []string{"/covers/../cairn.db", "/covers/%2e%2e%2fcairn.db", "/covers/nope.jpg"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		req.Host = "localhost:8893"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		// A path with ".." is cleaned and redirected by the mux; either way the file is never served.
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently &&
			rec.Code != http.StatusTemporaryRedirect {
			t.Errorf("%s = %d, want a refusal", path, rec.Code)
		}
	}
	_ = h
}
