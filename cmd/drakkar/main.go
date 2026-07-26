// drakkar is the production entry point for the Drakkar media server. It
// wires up logging, installs signal handling for graceful shutdown, and
// delegates all application startup to internal/app.Run.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/drakkar-media/drakkar/internal/app"
	"github.com/drakkar-media/drakkar/internal/observability"
)

func main() {
	// Cancel ctx on SIGINT/SIGTERM so app.Run can shut down its subsystems
	// (server, workers, DB connections) instead of the process being killed
	// mid-operation.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Write logs to both stdout and /app/data/logs/drakkar.log for the UI log viewer.
	logsDir := "/app/data/logs"
	if env := os.Getenv("DRAKKAR_LOGS_DIR"); env != "" {
		logsDir = env
	}
	logger := observability.NewWithFile(os.Stdout, observability.LevelInfo, logsDir)
	// context.Canceled is the expected result of a clean shutdown via the
	// signal handler above, so it is not treated as a startup/runtime failure.
	if err := app.Run(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error().Err(err).Msg("drakkar stopped")
		os.Exit(1)
	}
}
