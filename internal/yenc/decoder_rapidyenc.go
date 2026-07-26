//go:build rapidyenc && cgo

package yenc

import (
	"bytes"
	"errors"
	"strings"

	rapidyenc "github.com/mnightingale/rapidyenc"
)

// Sentinel errors returned by DecodeArticle/DecodeArticleWithInfo, identical
// to the purego build's errors of the same name (see decoder_purego.go) so
// callers can match on them regardless of which decoder was compiled in.
var (
	ErrMissingBegin = errors.New("yenc begin header missing")
	ErrMissingEnd   = errors.New("yenc end footer missing")
	ErrCRCMismatch  = errors.New("yenc crc mismatch")
)

// DecoderInfo identifies which decode implementation this binary was built
// with -- logged once at startup so it's easy to confirm the SIMD decoder is
// really active in a given deployment rather than a silently-reverted build.
func DecoderInfo() string {
	return "rapidyenc (" + rapidyenc.DecodeKernel() + ")"
}

// DecodeArticle decodes a yEnc-encoded NNTP article body via the
// rapidyenc SIMD decoder and verifies its CRC32 against the "=yend " footer,
// matching the purego build's function of the same name — see
// decoder_purego.go.
//
// Errors:
//   - ErrMissingBegin: no "=ybegin " header line found.
//   - ErrMissingEnd: no "=yend " footer line found after the header.
//   - ErrCRCMismatch: decoded payload fails CRC verification.
func DecodeArticle(body []byte) ([]byte, error) {
	return decodeArticleRapid(body, splitLines(body))
}

// DecodeArticleWithInfo decodes body and parses its yEnc header info from a
// single line-split pass, matching the purego build's function of the same
// name — see decoder_purego.go for why this avoids redundant re-splitting.
func DecodeArticleWithInfo(body []byte) ([]byte, PartInfo, error) {
	lines := splitLines(body)
	info, _ := parsePartInfoLines(lines)
	decoded, err := decodeArticleRapid(body, lines)
	if err != nil {
		return nil, PartInfo{}, err
	}
	return decoded, info, nil
}

// decodeArticleRapid decodes body via the rapidyenc C library and verifies
// its CRC32 against the footer found in lines.
//
// rapidyenc.Decoder expects an NNTP multiline-body stream terminated by the
// standard ".\r\n" end marker, which has already been stripped by the
// transport layer by the time body reaches this package — the terminator is
// re-appended here purely to satisfy the decoder's framing, not to signal
// real dot-stuffed content.
func decodeArticleRapid(body []byte, lines [][]byte) ([]byte, error) {
	stream := make([]byte, 0, len(body)+3)
	stream = append(stream, body...)
	stream = append(stream, '.', '\r', '\n')

	decoder := rapidyenc.NewDecoder(
		bytes.NewReader(stream),
		rapidyenc.WithStatusLineAlreadyRead(),
	)
	response, err := decoder.Next()
	if err != nil {
		return nil, mapRapidYencError(err)
	}
	if response == nil {
		return nil, ErrMissingBegin
	}
	if err := verifyExpectedCRCLines(lines, response.Data); err != nil {
		return nil, err
	}
	return response.Data, nil
}

// mapRapidYencError translates rapidyenc's own error types (and, as a
// fallback, its error message text for cases the library does not expose a
// typed error for) into this package's sentinel errors, so callers see the
// same error values regardless of which decoder build is active.
func mapRapidYencError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, rapidyenc.ErrDataMissing):
		return ErrMissingBegin
	case errors.Is(err, rapidyenc.ErrDataCorruption):
		if strings.Contains(err.Error(), "\"=yend\"") {
			return ErrMissingEnd
		}
		return ErrMissingEnd
	case errors.Is(err, rapidyenc.ErrCrcMismatch):
		return ErrCRCMismatch
	default:
		if strings.Contains(err.Error(), "\"=ybegin\"") {
			return ErrMissingBegin
		}
		if strings.Contains(err.Error(), "\"=yend\"") {
			return ErrMissingEnd
		}
		return err
	}
}
