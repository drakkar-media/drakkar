package observability

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
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

// New creates a zerolog logger that writes to w. If logsDir is non-empty,
// log lines are also tee'd to logsDir/drakkar.log for the /api/logs endpoint.
func New(w io.Writer, level Level) zerolog.Logger {
	return NewWithFile(w, level, "")
}

func NewWithFile(w io.Writer, level Level, logsDir string) zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339

	// DRAKKAR_LOG_FORMAT=console enables colored human-readable output.
	// The log file always receives raw JSON for the UI log viewer.
	useConsole := strings.ToLower(os.Getenv("DRAKKAR_LOG_FORMAT")) == "console"

	// stdoutWriter is either a plain writer or a colorized console writer.
	var stdoutWriter io.Writer = w
	if useConsole {
		stdoutWriter = zerolog.ConsoleWriter{
			Out:           w,
			TimeFormat:    "01-02 15:04:05",
			FieldsExclude: []string{"service"}, // already implied by the app name
		}
	}

	out := stdoutWriter
	if logsDir != "" {
		if err := os.MkdirAll(logsDir, 0o755); err == nil {
			logPath := filepath.Join(logsDir, "drakkar.log")
			if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				// stdoutWriter transforms bytes (ConsoleWriter or plain).
				// f receives the original JSON bytes — MultiWriter delivers both.
				out = io.MultiWriter(stdoutWriter, f)
			}
		}
	}

	zerolog.SetGlobalLevel(parseLevel(level))
	// No .Level() call here: that would pin this logger (and everything
	// derived from it via .With()) to a fixed level, making SetGlobalLevel
	// below a no-op for it. Leaving it unset means the effective level
	// always tracks the global level, so it can be changed at runtime
	// (see SetGlobalLevel) without restarting the process.
	logger := zerolog.New(out).With().Timestamp().Str("service", "drakkar").Logger()
	return logger
}

// SetGlobalLevel changes the effective log verbosity for every logger
// created via New/NewWithFile, without needing a restart. Invalid levels
// fall back to info.
func SetGlobalLevel(level Level) {
	zerolog.SetGlobalLevel(parseLevel(level))
}

// Recover logs and swallows a panic in a long-lived background goroutine so
// one bad iteration doesn't silently kill the whole worker with no
// diagnostic trail. Use as `defer observability.Recover("worker-name")` at
// the top of the goroutine body.
func Recover(name string) {
	if r := recover(); r != nil {
		slog.Error("goroutine panic recovered", "goroutine", name, "panic", r, "stack", string(debug.Stack()))
	}
}

// RecoverWithCleanup behaves like Recover, but additionally invokes cleanup
// with the recovered value — and only if a panic actually occurred. For
// goroutines that must release a resource (an in-flight-job slot, a result
// channel a caller is blocked reading from) even when the work they were
// doing panics instead of returning normally; without this, a panic mid-job
// would recover safely but silently skip whatever cleanup the normal
// return path would have done.
func RecoverWithCleanup(name string, cleanup func(recovered any)) {
	if r := recover(); r != nil {
		slog.Error("goroutine panic recovered", "goroutine", name, "panic", r, "stack", string(debug.Stack()))
		cleanup(r)
	}
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
