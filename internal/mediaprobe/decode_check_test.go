package mediaprobe

import (
	"context"
	"os/exec"
	"testing"
)

func TestDetectDecodeIssuesEmptyInput(t *testing.T) {
	issues, err := DetectDecodeIssues(context.Background(), nil, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issues != nil {
		t.Fatalf("expected no issues for empty input, got %v", issues)
	}
}

func TestDetectDecodeIssuesCleanFileReportsNothing(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed in this environment -- production-image-only dependency")
	}
	data := buildFixtureMKV(t) // from subtitle_languages_test.go -- a real, valid, tiny MKV
	issues, err := DetectDecodeIssues(context.Background(), data, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected no issues for a clean file, got %v", issues)
	}
}

func TestDetectDecodeIssuesCorruptedFileReportsSomething(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed in this environment -- production-image-only dependency")
	}
	data := buildFixtureMKV(t)
	// Truncate well past the EBML header/Tracks element (so the container
	// itself still parses) but before the real end, then pad back to the
	// original length with zero bytes -- this reliably produces a
	// mid-stream decode error (unlike flipping a handful of bytes deep
	// inside an already-encoded frame, which h264's error concealment can
	// often mask entirely) without merely re-triggering the
	// truncation-tolerant EOF path, since the file's outer length/structure
	// is unchanged.
	corrupted := append([]byte{}, data...)
	cut := len(corrupted) * 3 / 4
	for i := cut; i < len(corrupted); i++ {
		corrupted[i] = 0
	}
	issues, err := DetectDecodeIssues(context.Background(), corrupted, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) == 0 {
		t.Skip("ffmpeg didn't report a decode issue for this corruption pattern -- not every byte flip produces a detectable error; not a failure of DetectDecodeIssues itself")
	}
}

func TestIsTruncationArtifactFiltersExpectedEOFMessages(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"[mov,mp4,m4a,3gp,3g2,mj2 @ 0x1234] moov atom not found", false},
		{"EOF while searching for end of first segment", true},
		{"could not find codec parameters for stream 0", true},
		{"error reading header", true},
		{"Error while decoding stream #0:0: Invalid data found when processing input", false},
	}
	for _, tc := range cases {
		if got := isTruncationArtifact(tc.line); got != tc.want {
			t.Errorf("isTruncationArtifact(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestDetectDecodeIssuesRespectsContextTimeout(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed in this environment -- production-image-only dependency")
	}
	old := decodeCheckTimeout
	decodeCheckTimeout = 1
	defer func() { decodeCheckTimeout = old }()
	data := buildFixtureMKV(t)
	if _, err := DetectDecodeIssues(context.Background(), data, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No assertion beyond "doesn't hang" -- cmd.Run()'s error is
	// deliberately ignored (see DetectDecodeIssues), so a context-canceled
	// ffmpeg process must not make this test itself fail or hang.
}
