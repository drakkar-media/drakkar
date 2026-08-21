// Package observability provides the process's structured logging setup
// (zerolog-based, with runtime-adjustable verbosity and dual stdout/file
// output) plus panic-recovery helpers for long-lived background goroutines.
package observability

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Level names a logging verbosity accepted by NewWithFile and SetGlobalLevel.
type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// NewWithFile builds the application's root zerolog.Logger, writing to w and,
// when logsDir is non-empty, additionally appending raw JSON lines to
// <logsDir>/drakkar.log for the UI log viewer (the log file always gets JSON
// regardless of DRAKKAR_LOG_FORMAT). If the log directory or file cannot be
// created, logging silently falls back to w alone.
//
// The returned logger's level is left unset so it always tracks the process's
// global zerolog level — see the comment above the zerolog.New call — meaning
// SetGlobalLevel takes effect on it without recreating the logger.
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
			if f, err := newRotatingFile(logPath, defaultLogMaxSize, defaultLogMaxBackups); err == nil {
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

// defaultLogMaxSize/defaultLogMaxBackups bound drakkar.log's on-disk footprint
// (previously unbounded — a single deployment ran the file up to 978MB).
// Vars, not consts, so tests can shrink them.
var (
	defaultLogMaxSize    int64 = 100 * 1024 * 1024
	defaultLogMaxBackups       = 3
)

// rotatingFile is an io.Writer over a single log file that renames the
// current file to a numbered backup and reopens a fresh one once it would
// exceed maxSize, keeping at most maxBackups old files (oldest discarded).
// Safe for concurrent Write calls.
type rotatingFile struct {
	mu         sync.Mutex
	path       string
	maxSize    int64
	maxBackups int
	f          *os.File
	size       int64
}

func newRotatingFile(path string, maxSize int64, maxBackups int) (*rotatingFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	var size int64
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}
	return &rotatingFile{path: path, maxSize: maxSize, maxBackups: maxBackups, f: f, size: size}, nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.maxSize {
		// Rotation failure is non-fatal: keep writing to the current file
		// (unbounded growth) rather than silently dropping log lines.
		_ = r.rotate()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	for i := r.maxBackups; i >= 1; i-- {
		if i == r.maxBackups {
			_ = os.Remove(backupPath(r.path, i))
			continue
		}
		_ = os.Rename(backupPath(r.path, i), backupPath(r.path, i+1))
	}
	renameErr := os.Rename(r.path, backupPath(r.path, 1))
	// Reopen r.path unconditionally, even if the rename above failed --
	// r.f was already closed above, so returning early here without ever
	// reopening left every future Write silently failing forever (r.f stuck
	// on a closed handle). If the rename failed, r.path still holds the
	// old, over-size content and this just resumes appending to it, exactly
	// like the "rotation failure is non-fatal" comment in Write already
	// promises; if it succeeded, this creates the fresh post-rotation file.
	f, openErr := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if openErr != nil {
		if renameErr != nil {
			return renameErr
		}
		return openErr
	}
	r.f = f
	r.size = 0
	if renameErr != nil {
		if info, err := f.Stat(); err == nil {
			r.size = info.Size()
		}
		return renameErr
	}
	return nil
}

func backupPath(path string, n int) string {
	return fmt.Sprintf("%s.%d", path, n)
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
