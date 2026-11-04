package server

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
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
