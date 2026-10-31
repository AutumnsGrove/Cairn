// Command cairn is the service. S00 gives it one subcommand, because a walking skeleton should do one thing:
//
//	cairn serve     listen on 127.0.0.1:8893 and serve the health check and the embedded frontend
//	cairn version   print the version the health check reports
//
// verify, export and import arrive in later slices (S14 and S19).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AutumnsGrove/cairn/internal/config"
	"github.com/AutumnsGrove/cairn/internal/server"
	"github.com/AutumnsGrove/cairn/internal/version"
	"github.com/AutumnsGrove/cairn/internal/webui"
)

const usage = `cairn: a private review journal.

  cairn serve     run the service on 127.0.0.1:8893 (override with CAIRN_ADDR)
  cairn version   print the version
  cairn help      this text
`

func main() {
	// The signal context is the drain trigger: SIGINT and SIGTERM stop the listener and let an in-flight
	// request finish within the grace period.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Environ(), os.Stdout, os.Stderr))
}

// run is main without the process: it takes its context, its environment and its writers so a test can drive
// every path in-process, including the exit code.
func run(ctx context.Context, args []string, environ []string, stdout, stderr io.Writer) int {
	cmd := "help"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "serve":
		cfg, err := config.Load(environ)
		if err != nil {
			fmt.Fprintf(stderr, "cairn: %v\n", err)
			return 1
		}
		log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
		if err := server.Run(ctx, cfg, log, webui.FS()); err != nil {
			fmt.Fprintf(stderr, "cairn: %v\n", err)
			return 1
		}
		return 0

	case "version":
		fmt.Fprintln(stdout, version.Version)
		return 0

	case "help", "-h", "--help", "usage":
		fmt.Fprint(stdout, usage)
		return 0

	default:
		fmt.Fprintf(stderr, "cairn: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}
