// Package server is Cairn's HTTP surface: the health check, the embedded frontend, the Host and Origin
// guard, and the JSON error envelope every route answers with.
//
// Nothing here knows about reviews. The domain, the session engine and the agent arrive in later slices and
// are mounted behind the same guard, so the guard is built once and tested once (docs/BUILD.md 2).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/AutumnsGrove/cairn/internal/config"
	"github.com/AutumnsGrove/cairn/internal/version"
)

// Server timeouts. Every one is bounded: a stalled peer must not hold a connection forever
// (docs/STANDARDS.md 3.2). WriteTimeout is deliberately zero so a future WebSocket is not cut off by the
// server; per-request work carries its own deadline instead.
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

// New builds the whole HTTP surface. assets is the built frontend, rooted at the directory that holds
// index.html; a test passes an in-memory filesystem.
func New(cfg config.Config, log *slog.Logger, assets fs.FS) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if assets == nil {
		assets = emptyFS{}
	}
	s := &Server{cfg: cfg, log: log, assets: assets}
	return s.routes()
}

// Server holds what a handler needs. Its fields are read-only after New.
type Server struct {
	cfg    config.Config
	log    *slog.Logger
	assets fs.FS
}

// routes assembles the chain. Order matters: recover, then headers, then the Host check on everything, then
// the Origin check on state changes, then the route.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", getOnly(s.healthz))
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

// Run serves until ctx is cancelled or the listener fails, then drains.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger, assets fs.FS) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}

	srv := &http.Server{
		Handler:           New(cfg, log, assets),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	log.Info("cairn listening", "addr", cfg.Addr, "version", versionString(), "dev", cfg.Dev)

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve on %s: %w", cfg.Addr, err)
	case <-ctx.Done():
		// A fresh context on purpose: the request's context is already cancelled (that is what stopped the
		// server), and the drain needs its own deadline.
		shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		log.Info("cairn draining", "grace", shutdownGrace.String())
		if err := srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("drain: %w", err)
		}
		return nil
	}
}

// Gettext helpers: the health reply is the one shape an outside monitor parses, so it is written by hand
// rather than through an encoder that might reorder or add a field.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return
	}
	body, err := json.Marshal(struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}{Status: "ok", Version: versionString()})
	if err != nil { // a struct of two strings cannot fail; this keeps the linter honest
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		s.log.Debug("healthz write", "err", err)
	}
}

// apiNotFound answers an unknown /api path with the envelope, so a client never has to parse HTML.
func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
}

// getOnly refuses everything but GET and HEAD with the envelope and an Allow header, so a wrong method looks
// like every other error to the client.
func getOnly(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "That method is not allowed here.")
			return
		}
		h(w, r)
	})
}

// emptyFS stands in when a build has not been made, so the service still starts and the health check works.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
