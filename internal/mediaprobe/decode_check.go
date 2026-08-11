package mediaprobe

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// decodeCheckTimeout bounds how long a single ffmpeg decode-check may run.
var decodeCheckTimeout = 30 * time.Second

// DetectDecodeIssues attempts to decode up to maxSeconds of video/audio from
// data (a prefix of a media container's decoded bytes, not necessarily the
// whole file) via ffmpeg, and returns any error lines it reported. This
// exists to catch corruption none of Drakkar's existing checks can: yEnc/CRC
// validation only proves the ARTICLE bytes decoded correctly, and the
// container-magic check only proves the first few bytes look like a real
// video container -- neither actually decodes a single video frame, so a
// release with a genuinely corrupt video bitstream (but valid yEnc and a
// valid container header) passes both today.
//
// This is a HEURISTIC, advisory signal, not a hard verdict:
//   - A non-empty result does NOT necessarily mean the file is unplayable --
//     ffmpeg logs "error" lines for plenty of recoverable conditions
//     (concealed corrupt frames, minor stream irregularities) that a real
//     player plays through without visible issue.
//   - Because data is only a PREFIX, ffmpeg may legitimately run out of
//     input before decoding maxSeconds -- premature-EOF-shaped messages are
//     filtered out (see isTruncationArtifact) so a merely-too-short prefix
//     doesn't get misreported as corruption.
//   - An empty result does NOT prove the file has no corruption elsewhere
//     (only the prefix was checked).
//
// Callers must treat this as a "worth a human look" signal to log/surface,
// never as grounds to blocklist/reject on its own -- unlike
// DetectSubtitleLanguages/ProbeContainer, a wrong verdict here has real
// consequence (a false positive could get a perfectly good release
// discarded) and hasn't been validated against real corrupt-vs-clean
// samples the way the RAR5 decryption work was.
func DetectDecodeIssues(ctx context.Context, data []byte, maxSeconds int) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if maxSeconds <= 0 {
		maxSeconds = 5
	}
	checkCtx, cancel := context.WithTimeout(ctx, decodeCheckTimeout)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, "ffmpeg",
		"-v", "error",
		"-t", strconv.Itoa(maxSeconds),
		"-i", "-",
		"-f", "null",
		"-",
	)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Exit status is deliberately ignored: ffmpeg commonly exits non-zero
	// on a truncated prefix (premature EOF) even when nothing it printed
	// indicates real corruption -- the stderr line filtering below is the
	// actual signal, not the process exit code.
	_ = cmd.Run()

	var issues []string
	for _, line := range strings.Split(stderr.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isTruncationArtifact(line) {
			continue
		}
		issues = append(issues, line)
	}
	return issues, nil
}

// isTruncationArtifact reports whether line is one of ffmpeg's standard
// messages for "ran out of input data" -- expected and harmless when data
// is a bounded prefix rather than the complete file, not evidence of real
// corruption.
func isTruncationArtifact(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{
		"end of file",
		"eof",
		"could not find codec parameters",
		"error reading header",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
