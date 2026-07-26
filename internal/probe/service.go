// Package probe runs a fixed set of named health checks (e.g. connectivity
// to Usenet/indexers/Plex) and aggregates their pass/fail results into a
// single report for the status/health API.
package probe

import (
	"context"
	"fmt"
	"time"
)

// Result is the outcome of a single NamedProber run.
type Result struct {
	Name       string    `json:"name"`
	OK         bool      `json:"ok"`
	Detail     string    `json:"detail"`
	CheckedAt  time.Time `json:"checkedAt"`
	DurationMS int64     `json:"durationMs"`
}

// Report aggregates the Result of every prober from a single Service.Probe
// call.
type Report struct {
	CheckedAt time.Time `json:"checkedAt"`
	Results   []Result  `json:"results"`
}

// NamedProber defines a single health check a Service can run.
//
// Implementations must return promptly once ctx is canceled; Service bounds
// every call with its own per-probe timeout regardless of ctx's caller-set
// deadline.
type NamedProber interface {
	// Name identifies the prober in a Result and must be stable, since
	// callers key results off it.
	Name() string

	// Probe performs the health check and returns a non-nil error if and
	// only if the check failed.
	Probe(ctx context.Context) error
}

// Service runs a fixed set of NamedProbers and aggregates their results.
//
// Service is safe for concurrent use: probers is set once at construction
// and Probe holds no mutable shared state.
type Service struct {
	probers []NamedProber
	timeout time.Duration
}

// NewService creates a Service that runs probers in order, each bounded by a
// fixed 10-second timeout.
func NewService(probers ...NamedProber) *Service {
	return &Service{
		probers: probers,
		timeout: 10 * time.Second,
	}
}

// Probe runs every registered prober sequentially, each under its own
// context derived from ctx with the Service's fixed timeout, and collects
// one Result per prober.
//
// A failing prober never aborts the run — its failure is recorded as a
// non-OK Result with the error detail, and the remaining probers still run.
// The returned error is always nil; per-probe failures are reported through
// Report.Results, not the error return.
func (s *Service) Probe(ctx context.Context) (Report, error) {
	report := Report{
		CheckedAt: time.Now().UTC(),
		Results:   make([]Result, 0, len(s.probers)),
	}
	for _, prober := range s.probers {
		start := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, s.timeout)
		err := prober.Probe(probeCtx)
		cancel()

		result := Result{
			Name:       prober.Name(),
			OK:         err == nil,
			CheckedAt:  time.Now().UTC(),
			DurationMS: time.Since(start).Milliseconds(),
		}
		if err == nil {
			result.Detail = "ok"
		} else {
			result.Detail = fmt.Sprintf("error: %v", err)
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}
