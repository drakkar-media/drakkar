package observability

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

type Level string

const (
	LevelTrace Level = "trace"
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// New creates a zerolog logger that writes JSON to w. If logsDir is non-empty,
// log lines are also tee'd to logsDir/drakkar.log for the /api/logs endpoint.
func New(w io.Writer, level Level) zerolog.Logger {
	return NewWithFile(w, level, "")
}

func NewWithFile(w io.Writer, level Level, logsDir string) zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339
	out := w
	if logsDir != "" {
		if err := os.MkdirAll(logsDir, 0o755); err == nil {
			logPath := filepath.Join(logsDir, "drakkar.log")
			if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				out = io.MultiWriter(w, f)
			}
		}
	}
	logger := zerolog.New(out).With().Timestamp().Str("service", "drakkar").Logger()
	return logger.Level(parseLevel(level))
}

func parseLevel(level Level) zerolog.Level {
	switch strings.ToLower(string(level)) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}
