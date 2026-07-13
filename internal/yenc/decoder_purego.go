//go:build !rapidyenc || !cgo

package yenc

import (
	"bytes"
	"errors"
)

var (
	ErrMissingBegin = errors.New("yenc begin header missing")
	ErrMissingEnd   = errors.New("yenc end footer missing")
	ErrCRCMismatch  = errors.New("yenc crc mismatch")
)

// DecoderInfo identifies which decode implementation this binary was built
// with -- logged once at startup so a Dockerfile regression that silently
// drops back to this pure-Go path (e.g. losing the rapidyenc build tag or
// CGO_ENABLED) is visible instead of just showing up as unexplained CPU load.
func DecoderInfo() string {
	return "purego (no CGO/rapidyenc build tag)"
}

func DecodeArticle(body []byte) ([]byte, error) {
	return decodeArticleLines(splitLines(body))
}

// DecodeArticleWithInfo decodes body and parses its yEnc header info from a
// single line-split pass. Callers that need both (e.g. the segment cache,
// which always wants the decoded bytes and the header info together)
// previously called DecodeArticle and ParsePartInfo separately, each
// re-splitting the same ~700KB article body from scratch — decodeArticleLines
// also used to re-split it a third time internally via verifyExpectedCRC.
func DecodeArticleWithInfo(body []byte) ([]byte, PartInfo, error) {
	lines := splitLines(body)
	info, _ := parsePartInfoLines(lines)
	decoded, err := decodeArticleLines(lines)
	if err != nil {
		return nil, PartInfo{}, err
	}
	return decoded, info, nil
}

func decodeArticleLines(lines [][]byte) ([]byte, error) {
	start := -1
	end := -1
	for i, line := range lines {
		if bytes.HasPrefix(line, []byte("=ybegin ")) {
			start = i + 1
			continue
		}
		if bytes.HasPrefix(line, []byte("=yend ")) {
			end = i
			break
		}
	}
	if start == -1 {
		return nil, ErrMissingBegin
	}
	if end == -1 || end < start {
		return nil, ErrMissingEnd
	}

	var out []byte
	for _, rawLine := range lines[start:end] {
		// Skip yEnc control lines that may appear in the data section
		// (=ypart is placed immediately after =ybegin in multipart articles).
		if bytes.HasPrefix(rawLine, []byte("=y")) {
			continue
		}
		// NNTP dot-stuffing is already reversed at the wire level in
		// nntp.readMultilineBody before this function ever sees the body —
		// unstuffing again here would incorrectly strip a legitimate leading
		// byte from any decoded data line that happens to start with "..".
		line := rawLine
		for i := 0; i < len(line); i++ {
			b := line[i]
			if b == '=' {
				i++
				if i >= len(line) {
					break
				}
				b = line[i] - 64
			}
			out = append(out, b-42)
		}
	}
	if err := verifyExpectedCRCLines(lines, out); err != nil {
		return nil, err
	}
	return out, nil
}
