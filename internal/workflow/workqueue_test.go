package workflow

import (
	"testing"
	"time"
)

// TestWorkerOptionsSetsExplicitDrainDelay guards a real production incident
// (2026-08-25): DrainDelay was never set here, leaving it at Go's zero value
// (0) rather than gobullmq's documented 1-second default. NewWorker's own
// default-application only fires on a negative value (`if opts.DrainDelay <
// 0`), which zero never satisfies, so it fell through unset all the way to
// waitForJob's separate fallback (`if blockTimeout <= 0`), which backs off to
// 10ms instead -- a 100x busier BLMove poll than intended. Confirmed live:
// ~312 ops/sec sustained against Redis, blocked_clients always 0 (nothing
// was ever actually blocking), for the process's entire uptime. Asserting a
// literal positive duration here (not just "!= 0") catches a future refactor
// that reintroduces the zero value just as easily as one that flips the sign.
func TestWorkerOptionsSetsExplicitDrainDelay(t *testing.T) {
	opts := workerOptions(3)
	if opts.DrainDelay != 1*time.Second {
		t.Fatalf("expected DrainDelay to be explicitly set to 1s, got %v -- an unset (zero-value) DrainDelay silently degrades to a 10ms busy-poll instead of gobullmq's intended 1s default", opts.DrainDelay)
	}
}

// TestWorkerOptionsPreservesConcurrency guards the refactor that extracted
// workerOptions from Start: the workers count must still flow through to
// Concurrency, or every worker started via Start would silently run with
// whatever Concurrency's Go zero value implies instead of the configured
// pool size.
func TestWorkerOptionsPreservesConcurrency(t *testing.T) {
	opts := workerOptions(5)
	if opts.Concurrency != 5 {
		t.Fatalf("expected Concurrency 5, got %d", opts.Concurrency)
	}
}

func TestToBullPriority(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected int
	}{
		{name: "selected first", input: 0, expected: 1},
		{name: "background search lower", input: 10, expected: 11},
		{name: "negative clamps", input: -5, expected: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toBullPriority(tt.input); got != tt.expected {
				t.Fatalf("toBullPriority(%d) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}
