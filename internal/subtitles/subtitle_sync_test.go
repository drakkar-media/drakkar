package subtitles

import (
	"strings"
	"testing"
)

const sampleSRT = `1
00:00:01,000 --> 00:00:03,500
Hello there.

2
00:00:10,000 --> 00:00:12,000
Goodbye.
`

func TestMaxSubtitleTimestampMs(t *testing.T) {
	got := maxSubtitleTimestampMs([]byte(sampleSRT))
	want := int64(12_000)
	if got != want {
		t.Fatalf("maxSubtitleTimestampMs = %d, want %d", got, want)
	}
}

func TestMaxSubtitleTimestampMsEmptyBody(t *testing.T) {
	if got := maxSubtitleTimestampMs([]byte("not a subtitle file")); got != 0 {
		t.Fatalf("expected 0 for a body with no timestamps, got %d", got)
	}
}

func TestRescaleSubtitleTimestampsPreservesSeparatorAndScales(t *testing.T) {
	scaled := rescaleSubtitleTimestamps([]byte(sampleSRT), 2.0)
	got := string(scaled)
	if !strings.Contains(got, "00:00:02,000 --> 00:00:07,000") {
		t.Fatalf("expected first cue scaled 2x with comma separator preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "00:00:20,000 --> 00:00:24,000") {
		t.Fatalf("expected second cue scaled 2x, got:\n%s", got)
	}
}

func TestRescaleSubtitleTimestampsPreservesVTTDotSeparator(t *testing.T) {
	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHi\n"
	scaled := rescaleSubtitleTimestamps([]byte(vtt), 1.5)
	got := string(scaled)
	if !strings.Contains(got, "00:00:01.500 --> 00:00:03.000") {
		t.Fatalf("expected dot separator preserved and cue scaled 1.5x, got:\n%s", got)
	}
}

func TestMatchKnownFramerateRatio(t *testing.T) {
	cases := []struct {
		name     string
		measured float64
		wantOK   bool
	}{
		{"exact 25/23.976 PAL-vs-film mismatch", 25.0 / 23.976, true},
		{"within tolerance of 25/24", 25.0/24.0 + 0.001, true},
		{"no signal -- essentially matching", 1.0, false},
		{"wildly off -- different cut entirely, not a framerate signature", 1.8, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := matchKnownFramerateRatio(tc.measured)
			if ok != tc.wantOK {
				t.Fatalf("matchKnownFramerateRatio(%v) ok = %v, want %v", tc.measured, ok, tc.wantOK)
			}
		})
	}
}

// TestCorrectFramerateMismatchRescalesOnKnownRatio guards the actual
// end-to-end behavior: a subtitle authored for 25fps PAL played against a
// 23.976fps (film-rate) release should have every cue rescaled by the real
// 25/23.976 ratio.
func TestCorrectFramerateMismatchRescalesOnKnownRatio(t *testing.T) {
	// Subtitle's last cue implies a ~12s runtime; pretend the real video is
	// 12 * (23.976/25) ~= 11.51s -- i.e. the subtitle runs "too long" by
	// exactly the 25-vs-23.976 framerate ratio, the classic PAL/film mismatch.
	videoDuration := 12.0 * (23.976 / 25.0)
	corrected := correctFramerateMismatch([]byte(sampleSRT), videoDuration)
	got := string(corrected)
	if got == sampleSRT {
		t.Fatal("expected timestamps to be rescaled, body unchanged")
	}
	// The corrected last cue should land very close to the real video
	// duration (11.51s), not the original 12s.
	lastMs := maxSubtitleTimestampMs(corrected)
	wantMs := int64(videoDuration * 1000)
	diff := lastMs - wantMs
	if diff < -50 || diff > 50 {
		t.Fatalf("corrected last cue = %dms, want within 50ms of %dms", lastMs, wantMs)
	}
}

func TestCorrectFramerateMismatchLeavesUnchangedWithoutKnownSignal(t *testing.T) {
	// Video duration close to the subtitle's own -- no framerate mismatch,
	// nothing to correct.
	corrected := correctFramerateMismatch([]byte(sampleSRT), 12.0)
	if string(corrected) != sampleSRT {
		t.Fatal("expected body unchanged when there's no framerate-mismatch signal")
	}
}

func TestCorrectFramerateMismatchLeavesUnchangedWhenDurationUnknown(t *testing.T) {
	corrected := correctFramerateMismatch([]byte(sampleSRT), 0)
	if string(corrected) != sampleSRT {
		t.Fatal("expected body unchanged when video duration is unknown (<=0)")
	}
}

func TestCorrectFramerateMismatchLeavesUnchangedForDifferentCutNotFramerate(t *testing.T) {
	// A wildly different duration (e.g. subtitle for an extended cut) must
	// NOT be blindly rescaled -- that would make a non-linear mismatch look
	// falsely "fixed" instead of leaving it visibly wrong.
	corrected := correctFramerateMismatch([]byte(sampleSRT), 20.0)
	if string(corrected) != sampleSRT {
		t.Fatal("expected body unchanged for a duration ratio matching no known framerate signature")
	}
}
