package nntp

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestProviderCircuitBreakerTripsOnlyOnThrottleLikeErrors(t *testing.T) {
	b := newProviderCircuitBreaker()
	notFound := errors.New("article not found")
	for i := 0; i < 10; i++ {
		b.RecordFailure("p1", notFound)
	}
	if !b.Allow("p1") {
		t.Fatal("breaker should not trip on non-throttle-like errors, no matter how many")
	}
}

func TestProviderCircuitBreakerTripsAfterThresholdThrottleFailures(t *testing.T) {
	b := newProviderCircuitBreaker()
	throttle := errors.New("unexpected BODY status 430")
	for i := 0; i < breakerTripThreshold-1; i++ {
		b.RecordFailure("p1", throttle)
	}
	if !b.Allow("p1") {
		t.Fatal("breaker should not trip before reaching the threshold")
	}
	b.RecordFailure("p1", throttle)
	if b.Allow("p1") {
		t.Fatal("breaker should be tripped once the threshold is reached")
	}
}

func TestProviderCircuitBreakerSuccessFullyResets(t *testing.T) {
	b := newProviderCircuitBreaker()
	throttle := errors.New("unexpected BODY status 430")
	for i := 0; i < breakerTripThreshold; i++ {
		b.RecordFailure("p1", throttle)
	}
	if b.Allow("p1") {
		t.Fatal("expected breaker to be tripped")
	}
	b.RecordSuccess("p1")
	if !b.Allow("p1") {
		t.Fatal("expected success to fully reset the breaker")
	}
	// A fresh run of failures after reset must need the full threshold again,
	// not resume from wherever the counter was before the reset.
	for i := 0; i < breakerTripThreshold-1; i++ {
		b.RecordFailure("p1", throttle)
	}
	if !b.Allow("p1") {
		t.Fatal("breaker should not trip again until the threshold is met post-reset")
	}
}

func TestProviderCircuitBreakerCooldownDoublesAndCaps(t *testing.T) {
	b := newProviderCircuitBreaker()
	throttle := errors.New("unexpected BODY status 430")

	trip := func() *breakerState {
		for i := 0; i < breakerTripThreshold; i++ {
			b.RecordFailure("p1", throttle)
		}
		return b.state["p1"]
	}

	st := trip()
	if st.cooldown != breakerBaseCooldown*2 {
		t.Fatalf("expected cooldown to have doubled from base after first trip, got %s", st.cooldown)
	}

	// Force the breaker back open so the next failure can trip again, and
	// verify the cooldown keeps doubling up to the cap rather than growing
	// unbounded.
	for i := 0; i < 10; i++ {
		st.disabledUntil = time.Now().Add(-time.Second)
		b.RecordFailure("p1", throttle)
	}
	if st.cooldown > breakerMaxCooldown {
		t.Fatalf("cooldown must be capped at %s, got %s", breakerMaxCooldown, st.cooldown)
	}
}

// TestProviderCircuitBreakerDoesNotAccumulateStaleFailures guards against the
// regression found on live traffic: with many concurrent goroutines sharing
// one provider, a single sub-second hiccup can produce dozens of failures at
// once, and separately, occasional failures trickling in slowly over a long
// period must not be misread as one continuous outage. Only a failure within
// failureWindow of the last one should extend the streak.
func TestProviderCircuitBreakerDoesNotAccumulateStaleFailures(t *testing.T) {
	b := newProviderCircuitBreaker()
	throttle := errors.New("unexpected BODY status 430")
	for i := 0; i < breakerTripThreshold-1; i++ {
		b.RecordFailure("p1", throttle)
	}
	if !b.Allow("p1") {
		t.Fatal("breaker should not have tripped yet")
	}
	// Simulate a long gap since the last failure — old failures should no
	// longer count toward the threshold.
	b.state["p1"].lastFailureAt = time.Now().Add(-2 * failureWindow)
	b.RecordFailure("p1", throttle)
	if !b.Allow("p1") {
		t.Fatal("a failure after a long gap should restart the streak, not complete it")
	}
}

func TestProviderCircuitBreakerUnknownProviderIsAllowed(t *testing.T) {
	b := newProviderCircuitBreaker()
	if !b.Allow("never-seen") {
		t.Fatal("a provider with no recorded history should be allowed")
	}
}

func TestIsThrottleLikeErrRecognisesCircuitOpenAndStatus430(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("article not found"), false},
		{errors.New("Newshosting attempt 1: unexpected BODY status 430"), true},
		{ErrProviderCircuitOpen, true},
		{fmt.Errorf("provider1 attempt 1: %w", ErrProviderCircuitOpen), true},
	}
	for _, c := range cases {
		if got := isThrottleLikeErr(c.err); got != c.want {
			t.Errorf("isThrottleLikeErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
