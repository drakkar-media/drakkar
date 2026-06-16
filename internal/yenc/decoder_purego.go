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

func DecodeArticle(body []byte) ([]byte, error) {
	lines := splitLines(body)
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
		line := unstuffDotLine(rawLine)
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
	if err := verifyExpectedCRC(body, out); err != nil {
		return nil, err
	}
	return out, nil
}

func unstuffDotLine(line []byte) []byte {
	if len(line) >= 2 && line[0] == '.' && line[1] == '.' {
		return line[1:]
	}
	return line
}
