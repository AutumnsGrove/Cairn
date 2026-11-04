package main

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/AutumnsGrove/nova/decide"
	decideor "github.com/AutumnsGrove/nova/decide/openrouter"

	"github.com/AutumnsGrove/cairn/internal/agent"
	"github.com/AutumnsGrove/cairn/internal/banks"
	"github.com/AutumnsGrove/cairn/internal/config"
	"github.com/AutumnsGrove/cairn/internal/decisions"
	"github.com/AutumnsGrove/cairn/internal/providers"
	"github.com/AutumnsGrove/cairn/internal/server"
	"github.com/AutumnsGrove/cairn/internal/session"
	"github.com/AutumnsGrove/cairn/internal/store"
	"github.com/AutumnsGrove/cairn/internal/version"
)

// serve is the composition root: it opens the database, loads the shipped config, builds the agent and hands the
// whole thing to the server. A missing key or a broken YAML file degrades one feature with a logged warning and
// the service still starts (docs/BUILD.md invariant 13).
func serve(ctx context.Context, cfg config.Config, log *slog.Logger, assets fs.FS) error {
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(cfg.DataDir, "cairn.db")})
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err = db.VerifyTriggers(ctx); err != nil {
		return err
	}

	engine := session.New(session.Config{Store: db, Now: db.Now, NewID: db.NewID, ServerVersion: version.Version})

	finder := providers.NewFinder(providers.Config{
		TMDBKey: cfg.TMDBKey, DataDir: cfg.DataDir, Clock: clockFunc(db.Now),
		UserAgent: "cairn (Cairn, a private review journal)",
	})

	set, err := banks.LoadDir(filepath.Join(cfg.ConfigDir, "questions"))
	if err != nil {
		log.Warn("question banks are not usable; the card will not ask questions", "err", err.Error())
		set = nil
	}
	registry, err := decisions.Load(filepath.Join(cfg.ConfigDir, "decisions"))
	if err != nil {
		log.Warn("decisions config is not usable; the pre-read is off", "err", err.Error())
		registry = nil
	}
	var decider decide.Decider
	if cfg.OpenRouterKey != "" {
		decider = decideor.New(decideor.Config{APIKey: cfg.OpenRouterKey})
	}
	pre := decisions.New(decisions.Config{Registry: registry, Decider: decider, Logger: log})

	responder := server.NewResponder()
	live := server.NewLiveBus()
	a, err := agent.New(agent.Config{
		Store: db, Session: engine, Finder: finder, Banks: set, Decisions: pre,
		Model: cfg.Model, APIKey: cfg.OpenRouterKey,
		Responder: responder, Live: live.Publish, Logger: log, Now: db.Now, NewID: db.NewID,
	})
	if err != nil {
		return err
	}

	return server.Run(ctx, cfg, server.Deps{
		Engine: engine, Agent: a, Responder: responder, Live: live, Now: db.Now,
	}, log, assets)
}

// clockFunc lets a function be the clock providers asks for, without importing the world's fakes into the real
// binary.
type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }
