// Command cairn-dev runs the real service against the in-process world, with no network and no keys.
//
// It is the way to look at a screen or reproduce a bug without touching a provider: the server, the database
// and the agent are all the real ones, and only the outside services are fake. This command is never shipped
// (docs/BUILD.md 4).
//
// S00 boots the service and one trivial fake, which is enough to prove the wiring. Later slices point the
// providers at the world's URLs here.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AutumnsGrove/cairn/internal/config"
	"github.com/AutumnsGrove/cairn/internal/server"
	"github.com/AutumnsGrove/cairn/internal/webui"
	"github.com/AutumnsGrove/cairn/internal/world"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Environ(), os.Stderr); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "cairn-dev: %v\n", err)
		os.Exit(1)
	}
}

// run is main without the process, so a test can boot the whole dev stack in-process and stop it.
func run(ctx context.Context, environ []string, stderr io.Writer) error {
	cfg, err := config.Load(environ)
	if err != nil {
		return err
	}
	// Development is explicitly on: the friendly error body and the dev origin are allowed, and the Host
	// check is not relaxed, because a bug in it must show up here rather than on the potato.
	cfg.Dev = true

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// The world's clock is the real one here; a test passes a fixed clock to the fakes it builds itself.
	fake := world.Start("cairn-dev", world.RealClock{})
	defer fake.Close()
	log.Info("world ready", "service", fake.Name(), "url", fake.URL())

	// One line so a failure has a time, not a guess.
	log.Info("dev stack up", "addr", cfg.Addr, "note", "offline: every outside service is a fake")

	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx, cfg, log, webui.FS()) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Give the server's own drain a moment to finish before the world's listener is closed under it.
		select {
		case err := <-errCh:
			return err
		case <-time.After(15 * time.Second):
			return context.DeadlineExceeded
		}
	}
}
