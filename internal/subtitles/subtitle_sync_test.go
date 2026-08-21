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

// TestMaxSubtitleTimestampMsDoesNotOverflowOnPathologicallyLongHoursDigits
// guards a real gap: the timestamp regex's hours group used to allow an
// unbounded run of digits. A malformed cue with a very long digit run
// before the hours separator overflows strconv.ParseInt's int64 range in
// parseSubtitleTimestampMs, whose error is discarded -- the parsed hours
// value silently becomes garbage (via Go's signed-integer wraparound on the
// subsequent multiply), which can even go negative, corrupting the whole
// "largest timestamp in this subtitle" result used to detect/correct a
// framerate mismatch.
func TestMaxSubtitleTimestampMsDoesNotOverflowOnPathologicallyLongHoursDigits(t *testing.T) {
	// Deliberately the ONLY timestamp in the body -- maxSubtitleTimestampMs
	// takes the max across every match found, starting from 0. With the old
	// unbounded regex, ParseInt clamps an out-of-range hours string to
	// math.MaxInt64, and math.MaxInt64*3_600_000 wraps (two's complement) to
	// exactly -3_600_000 every time regardless of digit count -- which never
	// beats the initial max of 0, so the timestamp is silently dropped
	// entirely rather than merely miscalculated. With the fix, the hours
	// group can only ever capture up to 4 digits ("9999" from the tail of
	// the run), giving the correct bounded value below instead of 0.
	malformed := strings.Repeat("9", 30) + ":00:00,000"
	body := []byte(malformed + "\n")
	got := maxSubtitleTimestampMs(body)
	const want = int64(9999) * 3_600_000
	if got != want {
		t.Fatalf("maxSubtitleTimestampMs = %d, want %d (got 0 means the malformed cue's timestamp was silently dropped)", got, want)
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

// TestMatchKnownFramerateRatioPicksClosestNotFirst is the direct regression
// test for the gap confirmed live 2026-08-20: matchKnownFramerateRatio's own
// doc comment promises the CLOSEST known ratio, but it used to return
// whichever candidate was checked first in knownFramerateRatios' fixed list
// order. 25/23.976 (~=1.042708) and 25/24 (~=1.041667) are only ~0.00104
// apart -- closer to each other than 2x framerateRatioTolerance (0.005) --
// so their tolerance windows overlap heavily. A measurement landing in that
// overlap, genuinely closer to 25/24, must resolve to 25/24, not to
// 25/23.976 merely because that entry happens to come first in the list.
func TestMatchKnownFramerateRatioPicksClosestNotFirst(t *testing.T) {
	trueRatio := 25.0 / 24.0
	// Genuine measurement noise (a subtitle's last cue landing a moment
	// before the video's real end, per this function's own doc comment) --
	// small enough that 25/24 is still unambiguously the closer of the two
	// overlapping candidates.
	measured := trueRatio + 0.0002

	got, ok := matchKnownFramerateRatio(measured)
	if !ok {
		t.Fatalf("matchKnownFramerateRatio(%v): expected a match, got none", measured)
	}
	if got != trueRatio {
		t.Fatalf("matchKnownFramerateRatio(%v) = %v, want the closer ratio %v (25/24), not a farther one merely checked earlier in the list", measured, got, trueRatio)
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
