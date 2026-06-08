package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/hjongedijk/drakkar/internal/app"
	"github.com/hjongedijk/drakkar/internal/observability"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Write logs to both stdout and /app/data/logs/drakkar.log for the UI log viewer.
	logsDir := "/app/data/logs"
	if env := os.Getenv("DRAKKAR_LOGS_DIR"); env != "" {
		logsDir = env
	}
	logger := observability.NewWithFile(os.Stdout, observability.LevelInfo, logsDir)
	if err := app.Run(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error().Err(err).Msg("drakkar stopped")
		os.Exit(1)
	}
}
