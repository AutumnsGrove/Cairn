package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzRequestGuard throws arbitrary Host and Origin values at the guard.
//
// The guard parses two headers that an attacker fully controls, and a crash or a panic there would be a
// denial of service on the journal. It is also the one place where a subtle string comparison would be a
// security bug, so the properties below are checked on every input rather than on a list of examples:
//
//  1. no panic, ever;
//  2. the same input twice gives the same answer;
//  3. a host that is not on the allow-list is never served;
//  4. a state-changing request that was served did come from an allowed Origin.
func FuzzRequestGuard(f *testing.F) {
	seeds := []struct{ host, origin string }{
		{"localhost:8893", ""},
		{"localhost:8893", "http://localhost:8893"},
		{"localhost:8893", "https://lepotato.taild95077.ts.net:8893"},
		{"127.0.0.1:8893", "http://127.0.0.1:8893"},
		{"[::1]:8893", "http://[::1]:8893"},
		{"", ""},
		{"evil.example", "https://evil.example"},
		{"localhost:8893.evil.example", "https://localhost:8893.evil.example"},
		{"localhost:8893@evil.example", "http://localhost:8893@evil.example"},
		{"LOCALHOST:8893", "HTTP://LOCALHOST:8893"},
		{"localhost:8893 ", " http://localhost:8893"},
		{"localhost:8893/", "http://localhost:8893/"},
		{"lepotato.taild95077.ts.net:8893", "null"},
	}
	for _, s := range seeds {
		f.Add(s.host, s.origin)
	}

	cfg := testConfig()
	srv := &Server{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil)), assets: assets()}
	h := New(cfg, Deps{}, srv.log, assets())

	methods := []string{
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
	}

	f.Fuzz(func(t *testing.T, host, origin string) {
		if len(host) > 1024 || len(origin) > 1024 {
			t.Skip("a header longer than the server's own limit is refused before parsing")
		}
		for _, method := range methods {
			req := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/session", strings.NewReader(`{}`))
			req.Host = host
			if origin != "" {
				req.Header.Set("Origin", origin)
			}

			var first, second *httptest.ResponseRecorder
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("guard panicked on host %q origin %q: %v", host, origin, rec)
					}
				}()
				first = httptest.NewRecorder()
				h.ServeHTTP(first, req)
				second = httptest.NewRecorder()
				h.ServeHTTP(second, req.Clone(req.Context()))
			}()

			if first.Code != second.Code {
				t.Fatalf("non-deterministic: host %q origin %q gave %d then %d", host, origin, first.Code, second.Code)
			}

			if !srv.hostAllowed(host) && first.Code != http.StatusBadRequest {
				t.Fatalf("host %q was not refused: status %d", host, first.Code)
			}
			if first.Code == http.StatusOK && isStateChanging(req) && !srv.originAllowed(origin) {
				t.Fatalf("host %q origin %q was served although the origin is not allowed", host, origin)
			}
		}
	})
}
