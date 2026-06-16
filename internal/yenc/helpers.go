package yenc

import (
	"bytes"
	"hash/crc32"
	"strings"
)

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

func verifyExpectedCRC(body, decoded []byte) error {
	lines := splitLines(body)
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
