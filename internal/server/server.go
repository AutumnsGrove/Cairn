// Package server is Sepia's network face: the HTTP server, the session socket, the REST handlers and the Host
// and Origin guard in front of all of them (docs/slices/S09-server.md).
//
// It imports the session engine and the agent and nothing below them (docs/BUILD.md 4), so every fact a response
// carries comes through the engine. Nothing here writes a saved entry: the confirm is the engine's.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/AutumnsGrove/sepia/internal/agent"
	"github.com/AutumnsGrove/sepia/internal/config"
	"github.com/AutumnsGrove/sepia/internal/providers"
	"github.com/AutumnsGrove/sepia/internal/session"
	"github.com/AutumnsGrove/sepia/internal/version"
)

// Server timeouts. Every one is bounded: a stalled peer must not hold a connection forever
// (docs/STANDARDS.md 3.2). WriteTimeout is deliberately zero so a socket is not cut off by the server; per
// request work carries its own deadline instead.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownGrace     = 10 * time.Second
	// maxHeaderBytes bounds a request head. Above it the server answers 431 rather than reading on.
	maxHeaderBytes = 16 << 10
)

// versionString is what the health reply and the update panel report.
func versionString() string { return version.Version }

// Deps is what the network face is wired to. Both may be nil in a unit test that only wants the S00 boundary; a
// route that needs one then answers a stable "not built yet" rather than panicking.
type Deps struct {
	Engine *session.Engine
	Agent  *agent.Agent
	// Base is the context a turn runs under. It is not a request's context: a turn continues when the client
	// disappears (docs/PROTOCOL.md 1). Empty means context.Background(), which is what a test wants.
	Base context.Context
	// Now is the clock. Empty means the real one.
	Now func() time.Time
	// Responder bridges an agent's question and the client's tap. The service makes one and passes it to both
	// the agent and this package; nil means the hub makes its own, which is only useful in a test.
	Responder *Responder
	// Live carries the agent's live-only events to the sockets. Nil means there is no bus, which a test may not
	// care about.
	Live *LiveBus
}

// New builds the whole HTTP surface. assets is the built frontend, rooted at the directory that holds
// index.html; a test passes an in-memory filesystem.
func New(cfg config.Config, deps Deps, log *slog.Logger, assets fs.FS) http.Handler {
	return newServer(cfg, deps, log, assets).routes()
}

// newServer is New without the chain, so Run can keep the thing it needs to drain.
func newServer(cfg config.Config, deps Deps, log *slog.Logger, assets fs.FS) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if assets == nil {
		assets = emptyFS{}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	base := deps.Base
	if base == nil {
		base = context.Background()
	}
	s := &Server{
		cfg: cfg, log: log, assets: assets, engine: deps.Engine, model: deps.Agent, now: now, base: base,
		responder: deps.Responder,
	}
	s.hub = newHub(s)
	if deps.Live != nil {
		deps.Live.attach(s.hub.publishLive)
	}
	return s
}

// Server holds what a handler needs. Its fields are read-only after newServer.
type Server struct {
	cfg       config.Config
	log       *slog.Logger
	assets    fs.FS
	engine    *session.Engine
	model     *agent.Agent
	hub       *hub
	now       func() time.Time
	base      context.Context
	responder *Responder
}

// routes assembles the chain. Order matters: recover, then the access log, then headers, then the Host check on
// everything, then the Origin check on state changes, then the route.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("/healthz", getOnly(s.healthz))
	mux.Handle("/ws/sessions/{id}", http.HandlerFunc(s.socket))

	mux.Handle("/api/sessions", s.modeless(http.HandlerFunc(s.sessions)))
	mux.Handle("/api/sessions/{id}", getOnly(s.serveSession))
	mux.Handle("/api/sessions/{id}/media", postOnly(s.uploadMedia))
	mux.Handle("/api/entries", getOnly(s.listEntries))
	mux.Handle("/api/entries/{id}", getOnly(s.serveEntry))
	mux.Handle("/api/entries/{id}/revisions", getOnly(s.listRevisions))
	mux.Handle("/api/entries/{id}/delete", postOnly(s.deleteEntry))
	mux.Handle("/api/works/{id}", getOnly(s.serveWork))
	mux.Handle("/api/search", getOnly(s.search))
	mux.Handle("/api/export", postOnly(s.exportStub))
	mux.Handle("/api/version", getOnly(s.serveVersion))
	mux.Handle("/api/services", getOnly(s.serveServices))
	mux.Handle("/api/update/check", postOnly(s.notBuilt))
	mux.Handle("/api/update/available", getOnly(s.notBuilt))
	mux.Handle("/api/update/start", postOnly(s.notBuilt))
	mux.Handle("/api/update/status", getOnly(s.notBuilt))
	mux.Handle("/api/restart", postOnly(s.notBuilt))
	mux.Handle("/media/{id}", getOnly(s.notBuilt))
	mux.Handle("/covers/", getOnly(s.serveCover))

	mux.Handle("/api/", getOnly(s.apiNotFound))
	mux.Handle("/", getOnly(s.static))

	var h http.Handler = mux
	h = s.guard(h)
	h = s.securityHeaders(h)
	h = s.compress(h)
	h = s.recover(h)
	h = s.accessLog(h)
	return h
}

// modeless answers a route that needs the engine in a build that has none. It is a stable code, not a panic.
func (s *Server) modeless(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.engine == nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable", "Sepia is still starting. Try again in a moment.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// teardown closes every socket and waits for a running turn, bounded by the grace period. A hijacked connection is
// not tracked by http.Server, so the hub owns it.
func (s *Server) teardown(grace time.Duration, why string) {
	s.hub.closeAll(closeGoingAway, why)
	done := make(chan struct{})
	go func() {
		s.hub.turnsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		s.log.Warn("turns still running after the grace period", "grace", grace.String())
	}
}

// Run serves until ctx is cancelled or the listener fails, then drains.
func Run(ctx context.Context, cfg config.Config, deps Deps, log *slog.Logger, assets fs.FS) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	deps.Base = ctx
	s := newServer(cfg, deps, log, assets)

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}

	srv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	log.Info("sepia listening", "addr", cfg.Addr, "version", versionString(), "dev", cfg.Dev)

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		s.teardown(shutdownGrace, "the service is restarting")
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve on %s: %w", cfg.Addr, err)
	case <-ctx.Done():
		// A fresh context on purpose: the request's context is already cancelled (that is what stopped the
		// server), and the drain needs its own deadline.
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		log.Info("sepia draining", "grace", shutdownGrace.String())
		// Sockets first: a client that is waiting on a turn is told the service is going away, and a turn that
		// is already running is given the grace period to finish (docs/PROTOCOL.md 1).
		s.teardown(shutdownGrace, "the service is restarting")
		if err := srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("drain: %w", err)
		}
		return nil
	}
}

// healthz is the one shape an outside monitor parses, so it is written by hand rather than through an encoder
// that might reorder or add a field. It is liveness, not readiness: the database was checked at startup.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	body, err := json.Marshal(struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}{Status: "ok", Version: versionString()})
	if err != nil { // a struct of two strings cannot fail; this keeps the linter honest
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(body); err != nil {
		s.log.Debug("healthz write", "err", err)
	}
}

// apiNotFound answers an unknown /api path with the envelope, so a client never has to parse HTML.
func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
}

// notBuilt answers a route whose slice has not been built yet with a stable code, so the frontend can say one
// plain sentence (S13, S16, S18, S19).
func (s *Server) notBuilt(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "This part of Sepia is not built yet.")
}

// getOnly refuses everything but GET and HEAD with the envelope and an Allow header, so a wrong method looks
// like every other error to the client.
func getOnly(h http.HandlerFunc) http.Handler {
	return methodOnly(h, http.MethodGet, http.MethodHead)
}

func postOnly(h http.HandlerFunc) http.Handler {
	return methodOnly(h, http.MethodPost)
}

func methodOnly(h http.HandlerFunc, methods ...string) http.Handler {
	allow := strings.Join(methods, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, method := range methods {
			if r.Method == method {
				h(w, r)
				return
			}
		}
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "That method is not allowed here.")
	})
}

// serveCover serves a stored cover from the data directory. Only a bare file name is accepted, so a path
// traversal in a media or cover id is refused rather than read (S09 bad route).
func (s *Server) serveCover(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/covers/")
	if name == "" || name != path.Base(name) || strings.ContainsAny(name, `/\\`+"\x00") {
		writeError(w, http.StatusNotFound, "not_found", "No such cover.")
		return
	}
	dir := filepath.Join(s.cfg.DataDir, providers.CoverDir)
	// Both sides of the prefix check are resolved, because a temp directory on macOS is a symlink and the real
	// path is not the one os.TempDir handed out.
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such cover.")
		return
	}
	file := filepath.Join(dir, name)
	resolvedFile, err := filepath.EvalSymlinks(file)
	if err != nil || !strings.HasPrefix(resolvedFile, resolvedDir+string(os.PathSeparator)) {
		writeError(w, http.StatusNotFound, "not_found", "No such cover.")
		return
	}
	file = resolvedFile
	f, err := os.Open(file) //nolint:gosec // G703: file is resolved under the cover directory and checked above
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "No such cover.")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > providers.MaxCoverBytes {
		writeError(w, http.StatusNotFound, "not_found", "No such cover.")
		return
	}
	w.Header().Set("Content-Type", coverType(name))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func coverType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// requestID is short, random and not derived from anything the client sent, so it can be logged and shown
// without carrying a review or a title.
func requestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// emptyFS stands in when a build has not been made, so the service still starts and the health check works.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
