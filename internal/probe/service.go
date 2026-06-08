package probe

import (
	"context"
	"fmt"
	"time"
)

type Result struct {
	Name       string    `json:"name"`
	OK         bool      `json:"ok"`
	Detail     string    `json:"detail"`
	CheckedAt  time.Time `json:"checkedAt"`
	DurationMS int64     `json:"durationMs"`
}

type Report struct {
	CheckedAt time.Time `json:"checkedAt"`
	Results   []Result  `json:"results"`
}

type NamedProber interface {
	Name() string
	Probe(ctx context.Context) error
}

type Service struct {
	probers []NamedProber
	timeout time.Duration
}

func NewService(probers ...NamedProber) *Service {
	return &Service{
		probers: probers,
		timeout: 10 * time.Second,
	}
}

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
