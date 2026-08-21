package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// TestStartWithStartupDelaySkipsExtraRunWhenRunOnStartupFalse guards a real
// gap: StartWithStartupDelay used to fire its one-shot delayed run
// unconditionally, regardless of how recently the task last genuinely ran
// (e.g. a burst of redeploys/restarts each re-triggering a full storage/
// content/article-health/publishing-maintenance sweep). runOnStartup=false
// (the caller's shouldRunRecentOnStartup verdict) must suppress that run
// entirely -- the task's own regular interval loop (registered via Start)
// is untouched and still runs on schedule.
func TestStartWithStartupDelaySkipsExtraRunWhenRunOnStartupFalse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newRecurringTaskManager(ctx, zerolog.Nop())

	var calls atomic.Int32
	mgr.StartWithStartupDelay("test-task-skip", time.Hour, 20*time.Millisecond, false, func() {
		calls.Add(1)
	})

	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("expected the delayed startup run to be skipped when runOnStartup=false, got %d calls", got)
	}
}

// TestStartWithStartupDelayRunsExtraRunWhenRunOnStartupTrue is the mirror
// guard: runOnStartup=true must still fire the delayed run exactly like
// before this fix.
func TestStartWithStartupDelayRunsExtraRunWhenRunOnStartupTrue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newRecurringTaskManager(ctx, zerolog.Nop())

	var calls atomic.Int32
	mgr.StartWithStartupDelay("test-task-run", time.Hour, 20*time.Millisecond, true, func() {
		calls.Add(1)
	})

	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected the delayed startup run to fire exactly once when runOnStartup=true, got %d calls", got)
	}
}
