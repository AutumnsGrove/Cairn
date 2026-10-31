package server

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"
)

// envelope is the one error shape the API uses. The UI maps Code to its copy, so the code is stable and the
// message is for a person; Detail carries the internal reason only in development (docs/STANDARDS.md 4).
type envelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (e *envelope) Error() string { return e.Code + ": " + e.Message }

// writeError answers with the envelope. It is the only place a non-200 body is built, so the shape cannot
// drift between routes.
func writeError(w http.ResponseWriter, status int, code, message string) {
	body, err := json.Marshal(envelope{Code: code, Message: message})
	if err != nil {
		// Unreachable with the codes we use; falling back to Go's own text keeps a reply going out.
		http.Error(w, code, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body) // a client that hung up is not our error
}

// guard is the whole access rule, in one place: the Host header must name this service, on every request,
// and a state-changing request or a socket upgrade must also come from our own Origin.
//
// Both checks exist together on purpose. A DNS-rebinding attack makes Host and Origin agree with each other
// and disagree with reality: the Host check stops the rebinding, and the Origin check stops the page that
// was served under it from driving the API.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			s.log.Warn("refused: host", "host", r.Host, "path", r.URL.Path)
			writeError(w, http.StatusBadRequest, "bad_host", "This address is not Cairn.")
			return
		}
		if isStateChanging(r) && !s.originAllowed(r.Header.Get("Origin")) {
			s.log.Warn("refused: origin", "origin", r.Header.Get("Origin"), "path", r.URL.Path)
			writeError(w, http.StatusForbidden, "bad_origin", "This request did not come from Cairn.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed compares the request's Host against the allow-list exactly. No prefix, suffix or substring
// matching: localhost.example.com must never pass for localhost.
func (s *Server) hostAllowed(host string) bool {
	if host == "" {
		return false
	}
	for _, allowed := range s.cfg.AllowedHosts {
		if strings.EqualFold(host, allowed) {
			return true
		}
	}
	return false
}

// isStateChanging is every method that is not a read, plus a socket upgrade, which is a GET that becomes a
// long-lived write channel.
func isStateChanging(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return isUpgrade(r)
	default:
		return true
	}
}

func isUpgrade(r *http.Request) bool {
	if !headerHasToken(r.Header, "Connection", "upgrade") {
		return false
	}
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// originAllowed accepts only an http or https origin whose scheme://host:port is one of ours. A missing
// Origin, the literal "null", a bare host, or anything with a path is refused: fail closed.
func (s *Server) originAllowed(origin string) bool {
	if origin == "" || strings.EqualFold(origin, "null") {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false
	}
	for _, host := range s.cfg.AllowedHosts {
		if strings.EqualFold(u.Host, host) {
			return true
		}
	}
	return false
}

// securityHeaders sets the reply headers every route carries. The CSP differs by route: an API reply may
// load nothing at all, while the app shell is allowed its own bundled assets and no inline script.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
			h.Set("Cache-Control", "no-store")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

// recover turns a panic into a logged event and an envelope, rather than a dropped connection.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				s.log.Error("panic", "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// accessLog records one line per request. Path only: never a query string, never a body, never a title, so
// nothing of the operator's writing can end up in a log (docs/STANDARDS.md 2, invariant 9).
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.written,
			"ms", time.Since(start).Milliseconds(),
		)
	})
}

// statusRecorder remembers what was written so the access log and the tests can see it. It keeps the
// optional interfaces a handler may look for (Flusher, Hijacker), because the WebSocket in S09 needs them.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
	wrote   bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer, so a hijack still works through the log.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// compress gzips a reply when the client asked for it. It is skipped for a socket upgrade, because a hijacked
// connection writes its own frames, and for a reply that already chose an encoding.
func (s *Server) compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// acceptsEncoding honours a q=0 refusal, so "gzip;q=0" is not compressed here either.
		if isUpgrade(r) || !acceptsEncoding(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gz := &gzipResponder{ResponseWriter: w}
		defer gz.close()
		next.ServeHTTP(gz, r)
	})
}

type gzipResponder struct {
	http.ResponseWriter
	writer *gzip.Writer
	status int
	begun  bool
}

func (g *gzipResponder) WriteHeader(status int) {
	if g.begun {
		return
	}
	g.begun = true
	g.status = status
	// A reply that already chose an encoding (a precompressed asset) must not be compressed twice, and a
	// bodyless reply must not claim an encoding at all.
	if g.ResponseWriter.Header().Get("Content-Encoding") != "" ||
		status == http.StatusNoContent || status == http.StatusNotModified || status < 200 {
		g.ResponseWriter.WriteHeader(status)
		return
	}
	g.ResponseWriter.Header().Del("Content-Length")
	g.ResponseWriter.Header().Set("Content-Encoding", "gzip")
	g.writer = gzip.NewWriter(g.ResponseWriter)
	g.writer.Reset(g.ResponseWriter)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponder) Write(b []byte) (int, error) {
	if !g.begun {
		g.WriteHeader(http.StatusOK)
	}
	if g.writer == nil {
		return g.ResponseWriter.Write(b)
	}
	return g.writer.Write(b)
}

func (g *gzipResponder) close() {
	if g.writer != nil {
		_ = g.writer.Close()
	}
}

func (g *gzipResponder) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponder) Flush() {
	g.close()
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
