package yenc

import (
	"bytes"
	"hash/crc32"
	"strings"
)

// splitLines normalizes CRLF and lone-CR line endings to LF before splitting
// body into lines, since NNTP article bodies may use either depending on the
// posting server, and the yEnc header/data parsers assume a single
// consistent line boundary.
func splitLines(body []byte) [][]byte {
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	raw := strings.Split(normalized, "\n")
	out := make([][]byte, 0, len(raw))
	for _, line := range raw {
		out = append(out, []byte(line))
	}
	return out
}

// verifyExpectedCRCLines verifies decoded against the =yend crc32/pcrc32
// footer found in lines, for a caller that already has the article split
// into lines (e.g. DecodeArticle, which needs the same split for its own
// decoding pass) — avoids re-splitting the same ~700KB article body again.
func verifyExpectedCRCLines(lines [][]byte, decoded []byte) error {
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !bytes.HasPrefix(line, []byte("=yend ")) {
			continue
		}
		actual := crc32.ChecksumIEEE(decoded)
		if expected, ok := parseHexUint32(line, "pcrc32"); ok && actual != expected {
			return ErrCRCMismatch
		}
		if expected, ok := parseHexUint32(line, "crc32"); ok && actual != expected {
			return ErrCRCMismatch
		}
		return nil
	}
	return nil
}
