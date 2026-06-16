//go:build rapidyenc && cgo

package yenc

import (
	"bytes"
	"errors"
	"strings"

	rapidyenc "github.com/mnightingale/rapidyenc"
)

var (
	ErrMissingBegin = errors.New("yenc begin header missing")
	ErrMissingEnd   = errors.New("yenc end footer missing")
	ErrCRCMismatch  = errors.New("yenc crc mismatch")
)

func DecodeArticle(body []byte) ([]byte, error) {
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
	if err := verifyExpectedCRC(body, response.Data); err != nil {
		return nil, err
	}
	return response.Data, nil
}

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
