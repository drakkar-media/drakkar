// Package mediaprobe inspects a media container's own structure (via the
// ffprobe binary) to answer questions Drakkar can't get from the NZB/release
// metadata alone -- currently just "which subtitle languages are already
// embedded in this file". Kept separate from internal/database (which never
// shells out to an external binary, only parses bytes in-process) since this
// package's whole job is running an external process.
package mediaprobe

import (
	"bytes"
	"context"
	"strings"
	"time"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// probeTimeout bounds how long a single ffprobe invocation may run. A
// truncated/malformed prefix should fail fast, not hang a background
// goroutine indefinitely.
var probeTimeout = 20 * time.Second

// DetectSubtitleLanguages runs ffprobe against data -- a (possibly
// truncated) prefix of a media container's decoded bytes -- and returns the
// distinct ISO 639-1 language codes of any subtitle streams it can
// positively identify. data does not need to be a complete, valid file:
// ffprobe's Matroska demuxer parses sequentially, and a container's Tracks
// element (which carries per-stream language tags) normally sits well
// before the end of the file, so a bounded prefix is usually enough even
// though duration/full-stream info will be incomplete or missing.
//
// A nil result with a nil error means ffprobe ran but found no subtitle
// streams (or none with a confidently-identifiable language) -- a normal,
// common outcome. A non-nil error means ffprobe couldn't be run or couldn't
// parse the prefix at all (missing binary, truncated too early, unsupported
// container, etc.); callers must treat this exactly the same as "found
// nothing" for decision-making purposes -- this is a best-effort
// optimization to avoid redundant subtitle downloads, never a correctness
// requirement, so a failure here must never block or fail anything else.
func DetectSubtitleLanguages(ctx context.Context, data []byte) ([]string, error) {
	result, err := ProbeContainer(ctx, data)
	if err != nil {
		return nil, err
	}
	return result.SubtitleLanguages, nil
}

// ContainerProbe is everything Drakkar's best-effort container inspection
// currently extracts from a single ffprobe run over a media prefix.
type ContainerProbe struct {
	// SubtitleLanguages are the distinct ISO 639-1 codes of embedded
	// subtitle streams -- see DetectSubtitleLanguages's doc comment.
	SubtitleLanguages []string
	// DurationSeconds is the container's own declared duration, or 0 if
	// ffprobe couldn't determine it from the (possibly truncated) prefix.
	// For MKV this is normally available even from a prefix, since it
	// lives in the Segment Info element alongside Tracks near the front of
	// the file -- unlike Cues, which are commonly near the end. Used by
	// internal/subtitles to detect a framerate-mismatch scaling error in a
	// freshly-downloaded external subtitle (see subtitle_sync.go).
	DurationSeconds float64
}

// ProbeContainer runs a single ffprobe invocation against data -- a
// (possibly truncated) prefix of a media container's decoded bytes -- and
// returns everything Drakkar currently extracts from it. See
// DetectSubtitleLanguages for the truncated-input caveats; the same
// fail-safe contract applies here (a non-nil error, or zero-value fields on
// a nil error, both just mean "couldn't determine", never a fatal
// condition).
func ProbeContainer(ctx context.Context, data []byte) (ContainerProbe, error) {
	if len(data) == 0 {
		return ContainerProbe{}, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	result, err := ffprobe.ProbeReader(probeCtx, bytes.NewReader(data))
	if err != nil {
		return ContainerProbe{}, err
	}
	var out ContainerProbe
	if result.Format != nil {
		out.DurationSeconds = result.Format.DurationSeconds
	}
	seen := make(map[string]struct{}, len(result.Streams))
	for _, s := range result.Streams {
		if s == nil || s.CodecType != "subtitle" {
			continue
		}
		code := normalizeToISO6391(s.Tags.Language)
		if code == "" {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out.SubtitleLanguages = append(out.SubtitleLanguages, code)
	}
	return out, nil
}

// iso6392To1 maps ISO 639-2 (bibliographic and terminologic forms, as used
// by Matroska/ffprobe language tags) to ISO 639-1 two-letter codes -- the
// format Drakkar's subtitle providers/profiles use (see
// internal/subtitles.normalizeLanguage). Deliberately NOT delegating this to
// golang.org/x/text/language: its BCP47 parser is too lenient for
// validation purposes -- it happily accepts any well-formed-but-unassigned
// 2-3 letter subtag (e.g. "not", "qqq") as an "Exact" confidence match,
// which would make a garbage/corrupt ffprobe language tag look like a real
// language. A small explicit table of the languages Drakkar's subtitle
// providers actually support is safer: anything not in it returns "",
// falling back to "unknown" rather than risking a false match that wrongly
// skips a real subtitle download.
var iso6392To1 = map[string]string{
	"eng": "en",
	"fre": "fr", "fra": "fr",
	"ger": "de", "deu": "de",
	"spa": "es",
	"ita": "it",
	"por": "pt",
	"dut": "nl", "nld": "nl",
	"swe": "sv",
	"nor": "no", "nob": "no", "nno": "no",
	"dan": "da",
	"fin": "fi",
	"pol": "pl",
	"rus": "ru",
	"cze": "cs", "ces": "cs",
	"slo": "sk", "slk": "sk",
	"hun": "hu",
	"gre": "el", "ell": "el",
	"tur": "tr",
	"ara": "ar",
	"heb": "he",
	"jpn": "ja",
	"kor": "ko",
	"chi": "zh", "zho": "zh",
	"tha": "th",
	"vie": "vi",
	"ind": "id",
	"may": "ms", "msa": "ms",
	"hin": "hi",
	"ukr": "uk",
	"rum": "ro", "ron": "ro",
	"bul": "bg",
	"hrv": "hr",
	"srp": "sr",
	"slv": "sl",
	"est": "et",
	"lav": "lv",
	"lit": "lt",
	"cat": "ca",
	"baq": "eu", "eus": "eu",
	"glg": "gl",
	"ice": "is", "isl": "is",
}

// normalizeToISO6391 converts an ffprobe language tag to its ISO 639-1
// two-letter form. Returns "" for anything that isn't a recognized real
// language: an empty tag, the "und" (undetermined) sentinel Matroska uses
// when a track has no language set, or a code outside iso6392To1's table --
// treating any of these as "unknown" is the safe default, since claiming a
// language match that isn't real would wrongly skip a subtitle download.
func normalizeToISO6391(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || raw == "und" {
		return ""
	}
	if len(raw) == 2 {
		for _, v := range iso6392To1 {
			if v == raw {
				return raw
			}
		}
		return ""
	}
	return iso6392To1[raw]
}
