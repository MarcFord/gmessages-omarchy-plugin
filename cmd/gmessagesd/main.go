// Command gmessagesd is the background half of the Omarchy Google Messages
// plugin. It speaks the Google Messages web protocol via libgm and exposes a
// small newline-delimited JSON API on a Unix socket for the Quickshell panel.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/MarcFord/gmessages-omarchy-plugin/internal/daemon"
	"github.com/MarcFord/gmessages-omarchy-plugin/internal/store"
)

// version is overridden at build time with -ldflags.
var version = "dev"

func main() {
	var (
		showVersion = flag.Bool("version", false, "print version and exit")
		logLevel    = flag.String("log-level", "info", "trace, debug, info, warn, error")
		socketPath  = flag.String("socket", "", "override the Unix socket path")
	)
	// Subcommands are handled before flag parsing so `pair` can take its own
	// arguments without colliding with the daemon's.
	if len(os.Args) > 1 && os.Args[1] == "pair" {
		if err := runPair(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	level, err := zerolog.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid log level %q\n", *logLevel)
		os.Exit(2)
	}
	log := zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) {
		w.Out = os.Stderr
		w.TimeFormat = time.RFC3339
	})).Level(level).With().Timestamp().Logger()

	if err := run(log, *socketPath); err != nil {
		log.Error().Err(err).Msg("Fatal")
		os.Exit(1)
	}
}

func run(log zerolog.Logger, socketOverride string) error {
	paths, err := store.NewPaths()
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	socketPath := paths.SocketPath()
	if socketOverride != "" {
		socketPath = socketOverride
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := daemon.New(log, paths)
	if err := d.Start(ctx); err != nil {
		return err
	}
	defer d.Stop()

	log.Info().Str("version", version).Msg("gmessagesd started")
	return d.Serve(ctx, socketPath)
}
