// Package yenc decodes yEnc-encoded NNTP article bodies, the binary transfer
// encoding Usenet uses for posting/retrieving article payloads.
//
// Two decoding implementations exist, selected at compile time by build tag:
//
//   - decoder_purego.go (build tag "!rapidyenc || !cgo", the default): a
//     pure-Go decoder with no external dependencies, used whenever CGO is
//     disabled or the rapidyenc tag is not passed.
//   - decoder_rapidyenc.go (build tag "rapidyenc && cgo"): a CGO binding to
//     the SIMD-accelerated rapidyenc C library, used only in builds that
//     explicitly opt in via `-tags rapidyenc` with CGO enabled. It is
//     substantially faster for the segment-cache hot path but requires a
//     working C toolchain and libc at build time.
//
// Both implementations expose the same DecodeArticle, DecodeArticleWithInfo,
// and DecoderInfo functions and share this file's header parsing and
// helpers.go's CRC verification, so callers do not need to know which
// decoder is active. DecoderInfo is logged once at startup specifically so a
// build or Dockerfile regression that silently drops the rapidyenc tag or
// CGO_ENABLED is visible as a log line rather than only showing up later as
// unexplained CPU load.
//
// Decoding correctness constraint: NNTP dot-stuffing (a leading "." on a
// line being doubled to "..") must already be reversed by the transport
// layer (see nntp.readMultilineBody) before a body reaches either decoder.
// Un-stuffing it again here would incorrectly strip a legitimate leading
// byte from any decoded data line that happens to start with "..".
package yenc

import (
	"bytes"
	"encoding/hex"
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
	return parsePartInfoLines(splitLines(body))
}

// parsePartInfoLines is ParsePartInfo for a caller that already has the
// article split into lines — see verifyExpectedCRCLines for why.
func parsePartInfoLines(lines [][]byte) (PartInfo, bool) {
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

// parseHexUint32 extracts an 8-hex-digit key=value (e.g. crc32=deadbeef)
// from a yEnc footer line as a big-endian uint32.
func parseHexUint32(line []byte, key string) (uint32, bool) {
	value, ok := parseKeyValue(line, key)
	if !ok || len(value) != 8 {
		return 0, false
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 4 {
		return 0, false
	}
	return uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3]), true
}
