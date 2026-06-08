package yenc

import (
	"bytes"
	"strconv"
)

// PartInfo contains the decoded byte-range information from a yEnc multipart header.
type PartInfo struct {
	TotalSize int64 // =ybegin size=N  (total decoded file size)
	Begin     int64 // =ypart begin=N  (1-based start byte of this part)
	End       int64 // =ypart end=N    (1-based end byte of this part)
}

// Valid returns true when the header contains usable range information.
func (p PartInfo) Valid() bool {
	return p.End > p.Begin && p.Begin >= 1
}

// DecodedSize returns the number of decoded bytes in this segment.
func (p PartInfo) DecodedSize() int64 {
	if !p.Valid() {
		return 0
	}
	return p.End - p.Begin + 1
}

// DecodedStart returns the 0-based start offset within the file.
func (p PartInfo) DecodedStart() int64 {
	if !p.Valid() {
		return 0
	}
	return p.Begin - 1
}

// ParsePartInfo extracts the =ybegin and =ypart header values from a raw NNTP
// article body. It reads only the header lines and does not decode the payload,
// making it cheap to call during preflight checks.
func ParsePartInfo(body []byte) (PartInfo, bool) {
	lines := splitLines(body)
	var info PartInfo
	var hasBegin, hasPart bool
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("=ybegin ")) {
			if v, ok := parseKeyValue(line, "size"); ok {
				info.TotalSize, _ = strconv.ParseInt(v, 10, 64)
			}
			hasBegin = true
			continue
		}
		if bytes.HasPrefix(line, []byte("=ypart ")) {
			if v, ok := parseKeyValue(line, "begin"); ok {
				info.Begin, _ = strconv.ParseInt(v, 10, 64)
			}
			if v, ok := parseKeyValue(line, "end"); ok {
				info.End, _ = strconv.ParseInt(v, 10, 64)
			}
			hasPart = true
			continue
		}
		if hasBegin && hasPart {
			break
		}
		// Stop at the first non-header data line (after =ybegin/=ypart found).
		if hasBegin && len(line) > 0 && !bytes.HasPrefix(line, []byte("=y")) {
			break
		}
	}
	return info, hasBegin
}

// parseKeyValue extracts the value for key= from a yEnc header line.
// e.g. parseKeyValue("=ybegin part=1 size=716833 name=...", "size") → "716833", true
func parseKeyValue(line []byte, key string) (string, bool) {
	needle := []byte(key + "=")
	idx := bytes.Index(line, needle)
	if idx < 0 {
		return "", false
	}
	start := idx + len(needle)
	end := start
	for end < len(line) && line[end] != ' ' && line[end] != '\t' && line[end] != '\r' && line[end] != '\n' {
		end++
	}
	return string(line[start:end]), true
}
