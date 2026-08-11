package subtitles

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
)

// correctExternalSubtitleSync best-effort-corrects a framerate-mismatch
// sync error in a freshly-downloaded external subtitle (see
// DownloadCandidate) using the real video's own container duration -- see
// correctFramerateMismatch for exactly what is and isn't corrected.
// Deliberately only wired into the automatic-download path, not
// UploadSubtitle: a subtitle a user manually uploaded is their own choice
// to auto-modify without asking would be surprising.
//
// Any failure to determine the video's duration (repo error, never probed,
// ffprobe couldn't tell) degrades to "don't attempt correction" -- this is
// a best-effort improvement layered on top of a successful download, never
// a reason to fail or alter it beyond the specific case it can confidently
// fix.
func (s *Service) correctExternalSubtitleSync(ctx context.Context, libraryItemID int64, body []byte) []byte {
	duration, ok, err := s.repo.GetContainerDurationForLibraryItem(ctx, libraryItemID)
	if err != nil || !ok {
		return body
	}
	return correctFramerateMismatch(body, duration)
}

// subtitleTimestampPattern matches a single SRT/VTT cue timestamp in its
// universal HH:MM:SS,mmm / HH:MM:SS.mmm form (the form virtually every real
// subtitle file uses, including VTT files converted from SRT -- VTT's
// spec-permitted shorter MM:SS.mmm form is deliberately not matched here;
// a timestamp this doesn't recognize is just left untouched, which is safe
// by construction).
var subtitleTimestampPattern = regexp.MustCompile(`(\d{1,}):(\d{2}):(\d{2})[.,](\d{3})`)

// knownFramerateRatios are the duration ratios a subtitle authored for one
// common video framerate produces when played against a release encoded at
// a different common framerate -- the single most frequent real cause of a
// "constant" (linear-scaling, not shifting) subtitle sync problem. Listed
// as (from, to) pairs purely for readability; only the ratio itself is used.
var knownFramerateRatios = []float64{
	25.0 / 23.976, 23.976 / 25.0,
	25.0 / 24.0, 24.0 / 25.0,
	29.97 / 25.0, 25.0 / 29.97,
	29.97 / 23.976, 23.976 / 29.97,
	30.0 / 25.0, 25.0 / 30.0,
}

// framerateRatioTolerance bounds how close a measured duration ratio must
// be to one of knownFramerateRatios to be treated as a real framerate
// mismatch rather than coincidence or measurement noise (e.g. the video's
// last seconds being silence/credits the subtitle has no cue for).
const framerateRatioTolerance = 0.005

// maxSubtitleTimestampMs scans body for every cue timestamp
// subtitleTimestampPattern recognizes and returns the largest one found, in
// milliseconds -- the subtitle's own implied total duration. Returns 0 if
// no timestamp is found (an empty or unrecognized-format body).
func maxSubtitleTimestampMs(body []byte) int64 {
	matches := subtitleTimestampPattern.FindAllSubmatch(body, -1)
	var max int64
	for _, m := range matches {
		if ms := parseSubtitleTimestampMs(m[1], m[2], m[3], m[4]); ms > max {
			max = ms
		}
	}
	return max
}

func parseSubtitleTimestampMs(hours, minutes, seconds, millis []byte) int64 {
	h, _ := strconv.ParseInt(string(hours), 10, 64)
	m, _ := strconv.ParseInt(string(minutes), 10, 64)
	s, _ := strconv.ParseInt(string(seconds), 10, 64)
	ms, _ := strconv.ParseInt(string(millis), 10, 64)
	return h*3_600_000 + m*60_000 + s*1_000 + ms
}

// formatSubtitleTimestampMs renders totalMs back into HH:MM:SS<sep>mmm,
// using sep as whichever millisecond separator (',' or '.') the original
// timestamp used, so rescaling never changes a file's own SRT-vs-VTT
// formatting convention.
func formatSubtitleTimestampMs(totalMs int64, sep byte) []byte {
	if totalMs < 0 {
		totalMs = 0
	}
	h := totalMs / 3_600_000
	totalMs %= 3_600_000
	m := totalMs / 60_000
	totalMs %= 60_000
	s := totalMs / 1_000
	ms := totalMs % 1_000
	return []byte(fmt.Sprintf("%02d:%02d:%02d%c%03d", h, m, s, sep, ms))
}

// rescaleSubtitleTimestamps multiplies every cue timestamp in body by
// ratio, preserving each timestamp's own millisecond separator (comma for
// SRT, dot for VTT -- and some VTT files mix both in practice, so this is
// decided per-match, not per-file).
func rescaleSubtitleTimestamps(body []byte, ratio float64) []byte {
	return subtitleTimestampPattern.ReplaceAllFunc(body, func(match []byte) []byte {
		groups := subtitleTimestampPattern.FindSubmatch(match)
		ms := parseSubtitleTimestampMs(groups[1], groups[2], groups[3], groups[4])
		sep := byte(',')
		if idx := bytes.IndexAny(match, ".,"); idx >= 0 {
			sep = match[idx]
		}
		scaled := int64(float64(ms) * ratio)
		return formatSubtitleTimestampMs(scaled, sep)
	})
}

// matchKnownFramerateRatio returns the canonical ratio from
// knownFramerateRatios closest to measured, if any is within
// framerateRatioTolerance -- using the canonical value rather than the raw
// measured one avoids baking in noise from the measurement itself (e.g. the
// subtitle's last cue landing a second or two before the video's true end).
// ok is false if measured doesn't match any known framerate-conversion
// signature closely enough to be confident this is really a framerate
// mismatch rather than, say, a subtitle authored for a differently-cut
// release (different runtime entirely) -- which rescaling would make worse,
// not better.
func matchKnownFramerateRatio(measured float64) (ratio float64, ok bool) {
	for _, known := range knownFramerateRatios {
		if diff := measured - known; diff > -framerateRatioTolerance && diff < framerateRatioTolerance {
			return known, true
		}
	}
	return 0, false
}

// correctFramerateMismatch checks a freshly-downloaded external subtitle's
// own implied duration against the real video's container duration and, if
// their ratio closely matches a known framerate-conversion signature (e.g.
// the subtitle was authored for a 25fps PAL release but this file is
// 23.976fps), rescales every cue timestamp to correct it. Returns body
// unchanged (never an error) whenever there isn't a confident, specific
// signal to act on -- videoDurationSeconds <= 0 (unknown, e.g. never
// probed or ffprobe couldn't determine it), the subtitle has no
// recognizable timestamps, or the measured ratio doesn't match a known
// framerate pair closely enough. This is a best-effort improvement, not a
// correctness requirement: an undetected or wrongly-shaped mismatch must
// never block the subtitle from being saved as-is.
//
// Deliberately does NOT attempt to correct a true constant (additive, not
// scaling) offset -- e.g. dialogue starting a fixed few seconds late
// throughout with no change in total duration. That class of problem has no
// resource-cheap detection signal (it needs a reference point: audio
// analysis or a known-correct sync cue), unlike a framerate mismatch, which
// reveals itself purely through the duration ratio. See SESSION_TASKS.md's
// note on ALASS/audio-based sync for that harder, deferred case.
func correctFramerateMismatch(body []byte, videoDurationSeconds float64) []byte {
	if videoDurationSeconds <= 0 {
		return body
	}
	subtitleMs := maxSubtitleTimestampMs(body)
	if subtitleMs <= 0 {
		return body
	}
	videoMs := videoDurationSeconds * 1000
	measured := videoMs / float64(subtitleMs)
	ratio, ok := matchKnownFramerateRatio(measured)
	if !ok {
		return body
	}
	return rescaleSubtitleTimestamps(body, ratio)
}
