package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/AutumnsGrove/sepia/internal/config"
)

// These tests drive the S00 boundary end to end: the Host and Origin checks, the error envelope, the
// security headers, the health reply and the static page. Every route is checked on three routes: happy
// (the expected request), bad (hostile, malformed or enormous input) and sad (the world misbehaves).

func testConfig() config.Config {
	return config.Config{
		Addr:         "127.0.0.1:8893",
		AllowedHosts: []string{"localhost:8893", "127.0.0.1:8893", "[::1]:8893", "lepotato.taild95077.ts.net:8893"},
	}
}

// assets is a stand-in for the SvelteKit build, including the precompressed copies adapter-static writes.
// The gzip copy is really gzipped, because a test decodes it; the brotli copy is opaque to this server, which
// only ever serves it, so its bytes are arbitrary.
func assets() fstest.MapFS {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(":root{--bg:black}"))
	_ = zw.Close()

	return fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><html lang=\"en\"><title>Sepia</title><p>placeholder</p></html>")},
		"index.html.gz":   {Data: gz.Bytes()},
		"_app/app.css":    {Data: []byte(":root{--bg:black}")},
		"_app/app.css.gz": {Data: gz.Bytes()},
		"_app/app.css.br": {Data: []byte("BROTLI-BYTES")},
		"fonts/x.woff2":   {Data: []byte("wOF2")},
	}
}

func newTestServer(t *testing.T, cfg config.Config, fsys fstest.MapFS) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, Deps{}, log, fsys)
}

// do builds a request by hand so a test can set a Host or an Origin that a real client would never send.
func do(t *testing.T, h http.Handler, method, target, host, origin string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, body)
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("body is not JSON (%v): %s", err, rec.Body.String())
	}
}

// --- happy ---------------------------------------------------------------------------------------------

func TestHealthz(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodGet, "/healthz", "localhost:8893", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got map[string]any
	decode(t, rec, &got)
	if len(got) != 2 {
		t.Fatalf("healthz has %d fields, want exactly status and version: %v", len(got), got)
	}
	if got["status"] != "ok" {
		t.Errorf("status = %v, want ok", got["status"])
	}
	if _, ok := got["version"].(string); !ok {
		t.Errorf("version = %v, want a string", got["version"])
	}
	if rec.Body.String() != `{"status":"ok","version":"`+versionString()+`"}` {
		t.Errorf("body = %s, want the exact two-field object", rec.Body.String())
	}
}

func TestHealthzAcceptsEveryAllowedHost(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	for _, host := range testConfig().AllowedHosts {
		rec := do(t, h, http.MethodGet, "/healthz", host, "", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("Host %q: status %d, want 200", host, rec.Code)
		}
	}
}

func TestHealthzAnswersHead(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodHead, "/healthz", "localhost:8893", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned a body of %d bytes", rec.Body.Len())
	}
}

func TestStaticPage(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodGet, "/", "localhost:8893", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "placeholder") {
		t.Errorf("body = %q, want the built page", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestStaticAssetAndCompression(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodGet, "/_app/app.css", "localhost:8893", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want the hashed asset to be immutable", cc)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag on a static asset")
	}
}

// Reads are allowed from a hostile Origin: the Origin check is about state changes and socket upgrades, and
// every reply carries Cache-Control: no-store so a hostile page cannot read it anyway.
func TestReadIgnoresOrigin(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodGet, "/healthz", "localhost:8893", "https://evil.example", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestSecurityHeadersOnAPI(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodGet, "/healthz", "localhost:8893", "", nil)
	want := map[string]string{
		"Cache-Control":           "no-store",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "SAMEORIGIN",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// --- bad -----------------------------------------------------------------------------------------------

func TestHostRefused(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	hostile := map[string]string{
		"another site":             "evil.example",
		"suffix trick":             "localhost:8893.evil.example",
		"prefix trick":             "evil-localhost:8893",
		"rebinding to the tailnet": "lepotato.taild95077.ts.net.evil.example",
		"wrong port":               "localhost:9999",
		"empty":                    "",
		"ip and port trick":        "127.0.0.1:8893@evil.example",
		"ipv6 loopback alias":      "[::1]:9999",
	}
	paths := []string{"/healthz", "/", "/api/v1/anything", "/_app/app.css"}
	for name, host := range hostile {
		for _, p := range paths {
			t.Run(name+" "+p, func(t *testing.T) {
				rec := do(t, h, http.MethodGet, p, host, "", nil)
				if rec.Code == http.StatusOK {
					t.Fatalf("Host %q on %s was served", host, p)
				}
				var env envelope
				decode(t, rec, &env)
				if env.Code != "bad_host" {
					t.Errorf("code = %q, want bad_host", env.Code)
				}
			})
		}
	}
}

func TestOriginRefusedOnStateChange(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	hostile := map[string]string{
		"another site":         "https://evil.example",
		"same host, wrong sch": "ftp://localhost:8893",
		"prefix trick":         "https://localhost:8893.evil.example",
		"suffix trick":         "https://evil-localhost:8893",
		"null origin":          "null",
		"loopback wrong port":  "http://localhost:9999",
	}
	for name, origin := range hostile {
		t.Run(name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/api/v1/entries", "localhost:8893", origin, strings.NewReader(`{}`))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			var env envelope
			decode(t, rec, &env)
			if env.Code != "bad_origin" {
				t.Errorf("code = %q, want bad_origin", env.Code)
			}
		})
	}
}

// Fail closed: a state-changing request with no Origin at all is refused, because we cannot tell where it
// came from.
func TestOriginRequiredOnStateChange(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodPost, "/api/v1/entries", "localhost:8893", "", strings.NewReader(`{}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var env envelope
	decode(t, rec, &env)
	if env.Code != "bad_origin" {
		t.Errorf("code = %q, want bad_origin", env.Code)
	}
}

func TestOriginAllowedOnStateChange(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	for _, origin := range []string{"http://localhost:8893", "https://lepotato.taild95077.ts.net:8893"} {
		rec := do(t, h, http.MethodPost, "/api/v1/entries", "localhost:8893", origin, strings.NewReader(`{}`))
		if rec.Code == http.StatusForbidden {
			t.Errorf("Origin %q was refused; body %s", origin, rec.Body)
		}
	}
}

// websocketKey is the base64 nonce a client sends in Sec-WebSocket-Key.
func websocketKey() string {
	return base64.StdEncoding.EncodeToString([]byte("sepia-test-nonce"))
}

func TestWebSocketUpgradeRequiresOrigin(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())

	upgrade := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/session", nil)
		req.Host = "localhost:8893"
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		// A syntactically valid key, built from a low-entropy string on purpose: a random-looking literal
		// trips secret scanners, and this one is the client's nonce, not a credential.
		req.Header.Set("Sec-WebSocket-Key", websocketKey())
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := upgrade("https://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("hostile upgrade: status = %d, want 403", rec.Code)
	}
	if rec := upgrade(""); rec.Code != http.StatusForbidden {
		t.Fatalf("originless upgrade: status = %d, want 403", rec.Code)
	}
	// A same-origin upgrade is not refused by the guard. S00 has no socket yet, so the answer is the envelope
	// for an unknown API route, which proves the guard let it through.
	rec := upgrade("http://localhost:8893")
	if rec.Code == http.StatusForbidden {
		t.Fatalf("same-origin upgrade was refused: %s", rec.Body)
	}
}

func TestUnknownAPIPath(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodGet, "/api/v1/nope", "localhost:8893", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var env envelope
	decode(t, rec, &env)
	if env.Code != "not_found" || env.Message == "" {
		t.Errorf("envelope = %+v, want a not_found with a message", env)
	}
}

func TestWrongMethod(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodPost, "/healthz", "localhost:8893", "http://localhost:8893", strings.NewReader(`{}`))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("Allow = %q, want it to name GET", allow)
	}
	var env envelope
	decode(t, rec, &env)
	if env.Code != "method_not_allowed" {
		t.Errorf("code = %q, want method_not_allowed", env.Code)
	}
}

func TestPathTraversalRefused(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	for _, p := range []string{"/../etc/passwd", "/..%2f..%2fetc/passwd", "/_app/../../etc/passwd"} {
		rec := do(t, h, http.MethodGet, p, "localhost:8893", "", nil)
		if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "root:") {
			t.Fatalf("%s served a file outside the embed directory", p)
		}
	}
}

// The placeholder page must not be a hole: it is served with no scripts at all.
func TestPlaceholderHasNoScripts(t *testing.T) {
	h := newTestServer(t, testConfig(), fstest.MapFS{}) // no build present
	rec := do(t, h, http.MethodGet, "/", "localhost:8893", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with an unbuilt frontend", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Errorf("placeholder contains a script: %s", body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'none'") {
		t.Errorf("CSP = %q, want script-src 'none'", csp)
	}
}

// --- sad -----------------------------------------------------------------------------------------------

func TestEnormousHeaderRefused(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	srv := httptest.NewUnstartedServer(h)
	srv.Config.MaxHeaderBytes = 16 << 10
	srv.Start()
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "localhost:8893"
	req.Header.Set("X-Junk", strings.Repeat("a", 64<<10))
	resp, err := srv.Client().Do(req)
	if err != nil {
		return // the server may reset the connection instead of answering; that is a refusal too
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want the header to be refused", resp.StatusCode)
	}
}

func TestRunReportsAddressInUse(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	cfg := testConfig()
	cfg.Addr = addr
	cfg.AllowedHosts = []string{addr}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	errCh := make(chan error, 1)
	go func() { errCh <- Run(context.Background(), cfg, Deps{}, log, assets()) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run returned nil when the port was taken")
		}
		if !strings.Contains(err.Error(), addr) {
			t.Errorf("error %q does not name the address", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when the port was taken")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	cfg := testConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.AllowedHosts = []string{"localhost:8893", "127.0.0.1:0"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, Deps{}, log, assets()) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after the context was cancelled")
	}
}

// A request in flight when the shutdown starts is allowed to finish, within the drain deadline.
func TestShutdownDrainsInflight(t *testing.T) {
	block := make(chan struct{})
	h := newTestServer(t, testConfig(), assets())
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	})

	mux := http.NewServeMux()
	mux.Handle("/", h)
	mux.Handle("/slow", slow)

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	done := make(chan struct{})
	go func() {
		// A request with its own context, against this test's own loopback listener. Nothing leaves the
		// machine, and the guard's rule against dialling without a context is satisfied.
		reqCtx, reqCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer reqCancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+addr+"/slow", nil)
		if err != nil {
			close(done)
			return
		}
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	shutdownErr := make(chan error, 1)
	go func() { shutdownErr <- srv.Shutdown(ctx) }()

	close(block)
	select {
	case err := <-shutdownErr:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not finish")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the in-flight request was not drained")
	}
}

func TestNilLoggerAndAssetsDoNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("New panicked with an empty world: %v", r)
		}
	}()
	h := New(testConfig(), Deps{}, slog.New(slog.NewTextHandler(io.Discard, nil)), fstest.MapFS{})
	if rec := do(t, h, http.MethodGet, "/healthz", "localhost:8893", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestErrorEnvelopeIsStable(t *testing.T) {
	// The UI maps codes to copy, so the shape and the code set are part of the contract.
	if got := (&envelope{Code: "bad_host", Message: "nope"}).Error(); !strings.Contains(got, "bad_host") {
		t.Errorf("envelope.Error = %q", got)
	}
	body, err := json.Marshal(envelope{Code: "bad_host", Message: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"code":"bad_host","message":"nope"}`; string(body) != want {
		t.Errorf("envelope JSON = %s, want %s", body, want)
	}
}

// --- compression, conditions and the socket path ---------------------------------------------------------

func TestPrecompressedAssets(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())

	get := func(accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_app/app.css", nil)
		req.Host = "localhost:8893"
		if accept != "" {
			req.Header.Set("Accept-Encoding", accept)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("brotli is preferred when the client offers it", func(t *testing.T) {
		rec := get("gzip, br")
		if got := rec.Header().Get("Content-Encoding"); got != "br" {
			t.Fatalf("Content-Encoding = %q, want br", got)
		}
		if rec.Body.String() != "BROTLI-BYTES" {
			t.Errorf("body = %q, want the precompressed copy served as it is", rec.Body.String())
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Error("no Vary: Accept-Encoding")
		}
	})

	t.Run("gzip decodes to the original", func(t *testing.T) {
		rec := get("gzip")
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("body is not gzip: %v", err)
		}
		defer func() { _ = zr.Close() }()
		body, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != ":root{--bg:black}" {
			t.Errorf("decoded body = %q", body)
		}
	})

	t.Run("a refused encoding is not used", func(t *testing.T) {
		rec := get("gzip;q=0, br;q=0")
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want identity", got)
		}
		if rec.Body.String() != ":root{--bg:black}" {
			t.Errorf("body = %q", rec.Body.String())
		}
	})

	t.Run("no Accept-Encoding gets the original bytes", func(t *testing.T) {
		rec := get("")
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding = %q, want none", got)
		}
	})
}

func TestCompressionSkippedWithoutAcceptEncoding(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_app/app.css", nil)
	req.Host = "localhost:8893"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("compressed a client that did not ask for it")
	}
}

func TestConditionalGet(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	first := do(t, h, http.MethodGet, "/_app/app.css", "localhost:8893", "", nil)
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_app/app.css", nil)
	req.Host = "localhost:8893"
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 carried a body of %d bytes", rec.Body.Len())
	}
}

func TestMissingAssetIsEnvelope(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodGet, "/_app/missing.js", "localhost:8893", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var env envelope
	decode(t, rec, &env)
	if env.Code != "not_found" {
		t.Errorf("code = %q, want not_found", env.Code)
	}
}

// A route with no extension is a SPA route and gets the app shell, not a 404.
func TestSPAFallback(t *testing.T) {
	h := newTestServer(t, testConfig(), assets())
	rec := do(t, h, http.MethodGet, "/library", "localhost:8893", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the app shell", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "placeholder") {
		t.Errorf("body = %q, want index.html", rec.Body.String())
	}
}
