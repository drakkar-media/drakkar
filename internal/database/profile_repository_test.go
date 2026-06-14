package database

import (
	"encoding/json"
	"testing"
)

func TestQualityProfileUnmarshalAcceptsLegacySizeFields(t *testing.T) {
	var p QualityProfile
	if err := json.Unmarshal([]byte(`{"name":"Movie HD","minSizeMb":12,"maxSizeMb":48}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.MinMBPerMinute != 12 || p.MaxMBPerMinute != 48 {
		t.Fatalf("expected legacy fields to map to MB/minute values, got min=%d max=%d", p.MinMBPerMinute, p.MaxMBPerMinute)
	}
}

func TestQualityProfileUnmarshalPrefersNewPerMinuteFields(t *testing.T) {
	var p QualityProfile
	if err := json.Unmarshal([]byte(`{"name":"Movie HD","minSizeMb":12,"maxSizeMb":48,"minMbPerMinute":14,"maxMbPerMinute":55}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.MinMBPerMinute != 14 || p.MaxMBPerMinute != 55 {
		t.Fatalf("expected new fields to win, got min=%d max=%d", p.MinMBPerMinute, p.MaxMBPerMinute)
	}
}
