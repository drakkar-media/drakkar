package mediaprobe

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestNormalizeToISO6391(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"eng", "en"},
		{"ENG", "en"},
		{" eng ", "en"},
		{"dut", "nl"}, // ISO 639-2 bibliographic form for Dutch
		{"nld", "nl"}, // ISO 639-2 terminologic form for Dutch
		{"fre", "fr"},
		{"ger", "de"},
		{"und", ""},   // Matroska's "undetermined" sentinel
		{"", ""},
		{"not-a-real-language-code", ""},
	}
	for _, tc := range cases {
		if got := normalizeToISO6391(tc.in); got != tc.want {
			t.Errorf("normalizeToISO6391(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDetectSubtitleLanguagesEmptyInput(t *testing.T) {
	langs, err := DetectSubtitleLanguages(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if langs != nil {
		t.Fatalf("expected nil languages for empty input, got %v", langs)
	}
}

func TestDetectSubtitleLanguagesRealMKV(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed in this environment -- this is a production-image-only dependency, not a CI/test-runner one")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed -- needed to build the test fixture, not part of DetectSubtitleLanguages itself")
	}
	data := buildFixtureMKV(t)
	langs, err := DetectSubtitleLanguages(context.Background(), data)
	if err != nil {
		t.Fatalf("DetectSubtitleLanguages: %v", err)
	}
	if len(langs) != 1 || langs[0] != "en" {
		t.Fatalf("langs = %v, want [en]", langs)
	}
}

// buildFixtureMKV shells out to ffmpeg (only called when both ffmpeg and
// ffprobe are confirmed present, see the Skip guards above) to synthesize a
// tiny MKV with one English subtitle track, so the real ffprobe round-trip
// gets exercised end-to-end rather than only unit-testing the pure parsing
// logic in TestNormalizeToISO6391.
func buildFixtureMKV(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	srtPath := dir + "/sub.srt"
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nhello\n"), 0o644); err != nil {
		t.Fatalf("write srt fixture: %v", err)
	}
	outPath := dir + "/out.mkv"
	cmd := exec.Command("ffmpeg",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=1",
		"-i", srtPath,
		"-c:v", "libx264", "-c:s", "srt",
		"-metadata:s:s:0", "language=eng",
		outPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture build failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}
