package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/AutumnsGrove/sepia/internal/domain"
	"github.com/AutumnsGrove/sepia/internal/session"
)

// The internals that the route tests reach only sideways: the panic path, the response writers, the content
// type fallbacks and the "no frontend built" case. They are covered directly so a refactor cannot quietly drop
// one of them, and so the package's own coverage floor in CI means something.

func TestPanicBecomesAnEnvelope(t *testing.T) {
	cfg := testConfig()
	// The chain is assembled inside New, so build the recover links by hand for a handler that panics.
	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil)
	req.Host = "localhost:8893"

	s := &Server{cfg: cfg, log: slog.New(slog.DiscardHandler), assets: assets()}
	s.recover(boom).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not the envelope: %v (%s)", err, rec.Body)
	}
	if env.Code != "internal" {
		t.Errorf("code = %q, want internal", env.Code)
	}
}

// An aborted handler is not a bug to be reported: net/http uses the sentinel to close the connection, and
// turning it into a 500 would change the meaning of a hijacked socket.
func TestAbortHandlerIsRethrown(t *testing.T) {
	s := &Server{cfg: testConfig(), log: slog.New(slog.DiscardHandler), assets: assets()}
	h := s.recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("the abort was swallowed")
		}
	}()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestStatusRecorderKeepsOptionalInterfaces(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	var _ http.Flusher = sr
	if _, err := sr.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	sr.Flush()
	sr.WriteHeader(http.StatusTeapot) // ignored: the status was already sent
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the first status to win", rec.Code)
	}
	if sr.written != 5 {
		t.Errorf("written = %d, want 5", sr.written)
	}
	if sr.Unwrap() != http.ResponseWriter(rec) {
		t.Error("Unwrap did not return the underlying writer")
	}
}

func TestGzipResponderPassesThroughBodylessReplies(t *testing.T) {
	rec := httptest.NewRecorder()
	g := &gzipResponder{ResponseWriter: rec}
	g.WriteHeader(http.StatusNoContent)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q on a 204", got)
	}

	// A writing handler that never calls WriteHeader still gets a status.
	rec2 := httptest.NewRecorder()
	g2 := &gzipResponder{ResponseWriter: rec2}
	if _, err := g2.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	g2.close()
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec2.Code)
	}
	if g2.Unwrap() != http.ResponseWriter(rec2) {
		t.Error("Unwrap did not return the underlying writer")
	}
	g2.Flush()
}

func TestGzipRoundTripThroughTheMiddleware(t *testing.T) {
	s := &Server{cfg: testConfig(), log: slog.New(slog.DiscardHandler), assets: assets()}
	h := s.compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	req.Host = "localhost:8893"
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	defer func() { _ = zr.Close() }()
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"hello":"world"}` {
		t.Errorf("body = %q", body)
	}
}

func TestContentTypeFallbacks(t *testing.T) {
	cases := map[string]string{
		"a.woff2":       "font/woff2",
		"a.webmanifest": "application/manifest+json",
		"a.mjs":         "text/javascript; charset=utf-8",
		"a.unknown":     "application/octet-stream",
		"a.css":         "text/css",
	}
	for name, want := range cases {
		if got := contentType(name); !strings.HasPrefix(got, want) {
			t.Errorf("contentType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCacheControl(t *testing.T) {
	if got := cacheControlFor("_app/immutable/chunk.js"); !strings.Contains(got, "immutable") {
		t.Errorf("hashed asset: %q", got)
	}
	if got := cacheControlFor("index.html"); got != "no-cache" {
		t.Errorf("index: %q", got)
	}
}

func TestAcceptsEncoding(t *testing.T) {
	cases := []struct {
		header, encoding string
		want             bool
	}{
		{"", "gzip", false},
		{"gzip", "gzip", true},
		{"GZIP", "gzip", true},
		{"br, gzip", "gzip", true},
		{"gzip;q=0", "gzip", false},
		{"gzip;q=0.5", "gzip", true},
		{"identity", "br", false},
		{"br;q=0, gzip", "br", false},
	}
	for _, c := range cases {
		if got := acceptsEncoding(c.header, c.encoding); got != c.want {
			t.Errorf("acceptsEncoding(%q, %q) = %v, want %v", c.header, c.encoding, got, c.want)
		}
	}
}

func TestEtagMatches(t *testing.T) {
	cases := []struct {
		header, etag string
		want         bool
	}{
		{`"a"`, `"a"`, true},
		{`W/"a"`, `"a"`, true},
		{`"b", "a"`, `"a"`, true},
		{`"b"`, `"a"`, false},
		{"", `"a"`, false},
	}
	for _, c := range cases {
		if got := etagMatches(c.header, c.etag); got != c.want {
			t.Errorf("etagMatches(%q, %q) = %v", c.header, c.etag, got)
		}
	}
}

// A build that is present but has no index.html is still served as the app, not as a crash.
func TestDirectoryPathServesIndex(t *testing.T) {
	fsys := fstest.MapFS{
		"docs/index.html": {Data: []byte("<!doctype html><p>docs</p>")},
	}
	h := New(testConfig(), Deps{}, slog.New(slog.DiscardHandler), fsys)
	rec := do(t, h, http.MethodGet, "/docs/", "localhost:8893", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "docs") {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body)
	}
}

func TestNilAssetsAndLogger(t *testing.T) {
	h := New(testConfig(), Deps{}, nil, nil)
	rec := do(t, h, http.MethodGet, "/", "localhost:8893", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the placeholder", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("placeholder does not say how to build the frontend: %q", rec.Body)
	}
}

// The rate limiter is per session and in memory, so it has to forget a session that has gone quiet or the map grows
// with every conversation ever opened.
func TestRateLimiterForgetsAQuietSession(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	limiter := newRateLimiter(func() time.Time { return now })
	if ok, closeIt := limiter.allow("s1"); !ok || closeIt {
		t.Fatalf("first message refused: ok=%v close=%v", ok, closeIt)
	}
	now = now.Add(2 * time.Minute)
	if ok, _ := limiter.allow("s2"); !ok {
		t.Fatal("a new session was refused")
	}
	if _, ok := limiter.buckets["s1"]; ok {
		t.Error("a session quiet for two minutes was kept")
	}
	// She is not being punished for the gap: the old session starts fresh.
	if ok, _ := limiter.allow("s1"); !ok {
		t.Error("the quiet session was not allowed to start again")
	}
}

// The responder bridges a question to a tap, and a tap can arrive first: a phone that answers before the turn reaches
// Wait must not lose the answer, and an answer nobody waits for must not grow the map without bound.
func TestResponderKeepsAnEarlyAnswerAndHandsBackOne(t *testing.T) {
	r := NewResponder()
	r.deliver("q1", json.RawMessage(`{"value":"yes"}`))
	got, err := r.Wait(context.Background(), "s1", "q1")
	if err != nil || string(got) != `{"value":"yes"}` {
		t.Fatalf("early answer = %s, %v", got, err)
	}
	// The early answer is handed over once.
	if _, ok := r.early["q1"]; ok {
		t.Error("the early answer was not cleared after delivery")
	}

	// A waiter that is already blocked receives the answer through its channel.
	waited := make(chan string, 1)
	go func() {
		value, err := r.Wait(context.Background(), "s1", "q2")
		if err != nil {
			waited <- "error: " + err.Error()
			return
		}
		waited <- string(value)
	}()
	for i := 0; i < 100; i++ {
		r.mu.Lock()
		waiting := len(r.waiters) > 0
		r.mu.Unlock()
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	r.deliver("q2", json.RawMessage(`{"value":"no"}`))
	if value := <-waited; value != `{"value":"no"}` {
		t.Fatalf("delivered answer = %s", value)
	}

	// A turn that ends without an answer releases the caller instead of hanging on.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Wait(ctx, "s1", "q3"); !errors.Is(err, context.Canceled) {
		t.Errorf("wait on a cancelled turn = %v", err)
	}

	// Early answers are bounded: a client that taps and walks away cannot make the service hold them all.
	for i := 0; i < 80; i++ {
		r.deliver(fmt.Sprintf("q%d", 100+i), json.RawMessage(`null`))
	}
	if len(r.early) > 64 {
		t.Errorf("early answers = %d, want them bounded", len(r.early))
	}
}

// The hub's own bookkeeping: one turn per session, one message behind it, a stop that cancels, and the revises hint a
// confirmed revision needs. These are the rules the sockets share, so they are tested without a socket.
func TestHubHoldsOneTurnAndOneQueuedMessage(t *testing.T) {
	cfg := testConfig()
	s := &Server{
		cfg: cfg, log: slog.New(slog.DiscardHandler), assets: assets(),
		now: func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) },
	}
	h := newHub(s)
	ctx := context.Background()

	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()
	if err := h.startTurn(ctx, "s1", queuedMessage{text: "hello"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("start while closing = %v, want ErrClosed", err)
	}
	h.mu.Lock()
	h.closing = false
	h.mu.Unlock()

	// A session with a turn in flight accepts one message behind it, and refuses the third.
	turnCtx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.turns["s1"] = &turn{cancel: cancel, revises: ""}
	h.mu.Unlock()
	if err := h.startTurn(ctx, "s1", queuedMessage{text: "first in the queue"}); err != nil {
		t.Fatalf("queue one message: %v", err)
	}
	if err := h.startTurn(ctx, "s1", queuedMessage{text: "second in the queue"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("queue two messages = %v, want ErrQueueFull", err)
	}

	h.setRevises("s1", "entry-1")
	if got := h.getRevises("s1"); got != "entry-1" {
		t.Fatalf("revises = %q, want entry-1", got)
	}
	if got := h.getRevises("no-session"); got != "" {
		t.Errorf("revises for a session with no turn = %q", got)
	}

	// stop cancels the turn, and finishTurn forgets the session and its queue.
	h.stop("s1")
	h.stop("no-session")
	select {
	case <-turnCtx.Done():
	default:
		t.Error("stop did not cancel the turn")
	}
	h.finishTurn("s1")
	h.mu.Lock()
	_, still := h.turns["s1"]
	h.mu.Unlock()
	if still {
		t.Error("finishTurn left the turn behind")
	}

	// A per-session event with no socket on it is still written to the bus, and never panics.
	h.publishQueueState("s1", 1)
	h.publishError("s1", "internal", "Something went wrong. Your words are kept.", false)
	h.mu.Lock()
	h.bySession["s1"] = map[*socket]struct{}{}
	h.mu.Unlock()
	h.publishError("s1", "internal", "Something went wrong. Your words are kept.", true)
}

// The wire keeps a medium inside the contract's closed set and shortens text without splitting a character. Both are
// the kind of small mapping a refactor drops silently, so they are asserted directly.
func TestWireMediumStaysInTheClosedSet(t *testing.T) {
	for _, medium := range []string{"film", "tv", "book", "game", "album", "podcast", "theatre", "other"} {
		if got := wireMedium(medium); got != medium {
			t.Errorf("wireMedium(%q) = %q", medium, got)
		}
	}
	if got := wireMedium(""); got != "" {
		t.Errorf("wireMedium(\"\") = %q, want an empty medium", got)
	}
	// A provider that invents a medium is shown as other rather than breaking the card.
	if got := wireMedium("light novel"); got != "other" {
		t.Errorf("wireMedium(unknown) = %q, want other", got)
	}
}

func TestClipRunesShortensWithoutSplitting(t *testing.T) {
	if got := clipRunes("short", 10); got != "short" {
		t.Errorf("clipRunes kept %q", got)
	}
	long := strings.Repeat("a", 300)
	if got := clipRunes(long, 100); len(got) != 100 {
		t.Errorf("clipRunes length = %d, want 100", len(got))
	}
	// A character outside ASCII is four bytes, and half of one would be mojibake on her screen.
	if got := clipRunes(strings.Repeat("é", 20), 5); got != "ééééé" {
		t.Errorf("clipRunes split a character: %q", got)
	}
}

// The "why" sheet shows money as two numbers, never one: what the call consumed, and how much of it a plan covered.
// A screen drawing only the total would bill her for an allowance she already paid for.
func TestWhySheetSplitsCoveredFromSpent(t *testing.T) {
	card := session.Card{
		PrereadCost: domain.CostOf(0.0002, 0, domain.CostSourceProvider),
		TurnCost:    domain.CostOf(0.0021, 0.0021, domain.CostSourceAllowance),
	}
	why := whyOf(card)
	if why == nil {
		t.Fatal("a card with money on it has no why sheet")
	}
	raw, err := json.Marshal(why)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		PrereadCost struct {
			TotalUSD   float64 `json:"total_usd"`
			CoveredUSD float64 `json:"covered_usd"`
			Estimated  bool    `json:"estimated"`
		} `json:"preread_cost"`
		TurnCost struct {
			TotalUSD   float64 `json:"total_usd"`
			CoveredUSD float64 `json:"covered_usd"`
			Estimated  bool    `json:"estimated"`
		} `json:"turn_cost"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("why sheet: %v\n%s", err, raw)
	}
	if got.PrereadCost.TotalUSD != 0.0002 || got.PrereadCost.CoveredUSD != 0 {
		t.Errorf("preread cost = %+v", got.PrereadCost)
	}
	if got.TurnCost.TotalUSD != 0.0021 || got.TurnCost.CoveredUSD != 0.0021 {
		t.Errorf("turn cost = %+v", got.TurnCost)
	}

	// A price-list estimate says so, because a guessed number is not a receipt.
	card.TurnCost = domain.CostOf(0.003, 0, domain.CostSourceCatalog)
	if why := whyOf(card); why == nil || !why.TurnCost.Estimated {
		t.Errorf("catalog cost = %+v, want it marked as an estimate", why)
	}

	// A card with nothing to explain still has no sheet at all.
	if why := whyOf(session.Card{}); why != nil {
		t.Errorf("an empty card produced a sheet: %+v", why)
	}
}
