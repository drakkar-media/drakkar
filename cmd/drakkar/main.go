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
	"time"

	"github.com/drakkar-media/drakkar/internal/app"
	"github.com/drakkar-media/drakkar/internal/observability"
	"github.com/rs/zerolog"
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

	// Deliberately explicit rather than relying on Go's implicit TZ-env-var
	// startup behavior: that behavior is real (time.Local is initialized
	// from $TZ automatically), but it depends on the container image
	// actually shipping tzdata for a named zone like "Europe/Amsterdam" to
	// resolve -- a minimal/scratch base image without it would silently
	// fail to apply the requested zone with no visible error at all. This
	// makes the outcome explicit and logged either way: a named zone that
	// fails to load is a loud warning (falls back to UTC, not a silent
	// wrong-timezone), and no TZ set at all is a deliberate, logged UTC
	// default rather than "whatever the base image happens to default to".
	configureTimeZone(logger)

	// context.Canceled is the expected result of a clean shutdown via the
	// signal handler above, so it is not treated as a startup/runtime failure.
	if err := app.Run(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error().Err(err).Msg("drakkar stopped")
		os.Exit(1)
	}
}

// configureTimeZone sets time.Local from $TZ, falling back to UTC (and
// logging why) whenever TZ is unset or names a zone the container's tzdata
// can't resolve. Every internal Drakkar timestamp (log lines, scheduled
// task cadence, calendar/history date math) reads time.Local through
// time.Now(), so this is the one place that needs to get it right rather
// than trusting the runtime's own silent-on-failure default.
func configureTimeZone(logger zerolog.Logger) {
	tz := os.Getenv("TZ")
	if tz == "" {
		time.Local = time.UTC
		logger.Info().Str("timezone", "UTC").Msg("startup: TZ not set, defaulting to UTC")
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		time.Local = time.UTC
		logger.Warn().Err(err).Str("tz", tz).Msg("startup: TZ set but could not be loaded (missing tzdata?) — falling back to UTC")
		return
	}
	time.Local = loc
	logger.Info().Str("timezone", tz).Msg("startup: timezone configured")
}
