package app

import (
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
)

func TestShouldRunFullSegmentCheckOnlyForRecentItems(t *testing.T) {
	now := time.Now()
	if !shouldRunFullSegmentCheck(now, database.DeepHealthCandidate{CreatedAt: now.Add(-24 * time.Hour)}) {
		t.Fatal("day-old item should get the full segment check")
	}
	if shouldRunFullSegmentCheck(now, database.DeepHealthCandidate{CreatedAt: now.Add(-30 * 24 * time.Hour)}) {
		t.Fatal("month-old item should not be fully re-STATed on every pass")
	}
}
