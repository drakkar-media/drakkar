package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/systembackup"
)

const (
	defaultRequestBodyLimitBytes = int64(1 << 20)
	authRequestBodyLimitBytes    = int64(8 << 10)
	bulkRequestBodyLimitBytes    = int64(8 << 20)
	subtitleUploadLimitBytes     = int64(2 << 20)
	multipartOverheadBytes       = int64(1 << 20)
)

var errRequestBodyTooLarge = errors.New("request body too large")

func requestBodyLimitMiddleware(status StatusService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil || r.Body == http.NoBody {
				next.ServeHTTP(w, r)
				return
			}
			limit := requestBodyLimit(r, configuredNZBUploadLimit(status))
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			if r.ContentLength > limit {
				writeRequestBodyTooLarge(w, r.URL.Path)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func configuredNZBUploadLimit(status StatusService) int64 {
	if status != nil {
		if limit := status.Status().NZBUploadLimitBytes; limit > 0 {
			return limit
		}
	}
	return config.DefaultNZBUploadLimitBytes
}

func requestBodyLimit(r *http.Request, nzbUploadLimit int64) int64 {
	path := r.URL.Path
	switch {
	case path == "/api/auth/login",
		path == "/api/setup/complete",
		path == "/api/users",
		strings.HasPrefix(path, "/api/users/") && strings.HasSuffix(path, "/password"):
		return authRequestBodyLimitBytes
	case isSABAPIPath(path):
		if isMultipartRequest(r) {
			return nzbUploadLimit + multipartOverheadBytes
		}
		return defaultRequestBodyLimitBytes
	case path == "/api/nzbs/import",
		strings.HasPrefix(path, "/api/library/") && strings.HasSuffix(path, "/manual-import/upload"):
		if isMultipartRequest(r) {
			return nzbUploadLimit + multipartOverheadBytes
		}
		return nzbUploadLimit
	case strings.HasPrefix(path, "/api/subtitles/") && strings.HasSuffix(path, "/upload"):
		if isMultipartRequest(r) {
			return subtitleUploadLimitBytes + multipartOverheadBytes
		}
		return subtitleUploadLimitBytes
	case path == "/api/settings",
		path == "/api/custom-formats/import",
		path == "/api/release-block-rules/import":
		return bulkRequestBodyLimitBytes
	case path == "/api/system/backups/upload":
		return systembackup.MaxArchiveBytes + multipartOverheadBytes
	default:
		return defaultRequestBodyLimitBytes
	}
}

func isMultipartRequest(r *http.Request) bool {
	return strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data")
}

func isSABAPIPath(path string) bool {
	return path == "/sabnzbd/api" || path == "/api/sabnzbd/api" || path == "/dav/api"
}

func writeRequestBodyTooLarge(w http.ResponseWriter, path string) {
	if isSABAPIPath(path) {
		respondJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"status": false,
			"error":  errRequestBodyTooLarge.Error(),
		})
		return
	}
	respondJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": errRequestBodyTooLarge.Error()})
}

func isRequestBodyTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

// decodeJSONBody consumes the complete bounded body and rejects trailing JSON.
// Consuming through EOF ensures chunked requests cannot hide excess data after
// an otherwise valid first value.
func decodeJSONBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return io.EOF
	}
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func decodeOptionalJSONBody(r *http.Request, dst any) error {
	err := decodeJSONBody(r, dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
