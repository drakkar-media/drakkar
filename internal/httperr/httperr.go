// Package httperr classifies HTTP error responses from upstream services
// (Seerr, NZBHydra2) that sit behind Cloudflare or a reverse proxy. Both
// clients used to carry byte-for-byte identical copies of this logic —
// this is the single shared implementation.
package httperr

import (
	"fmt"
	"net/http"
	"strings"
)

// ClassifyStatus builds an error message for a non-2xx response, calling
// out Cloudflare-specific status codes (520-524) and gateway errors
// distinctly since those usually mean "upstream is temporarily unreachable"
// rather than a real API error. service is the upstream name (e.g. "seerr",
// "nzbhydra2"); action describes what was being attempted (e.g. "search",
// "create request") for the error message.
func ClassifyStatus(service, action string, statusCode int, body []byte) error {
	snippet := SummarizeBody(body)
	switch statusCode {
	case 520, 521, 522, 523:
		if snippet != "" {
			return fmt.Errorf("%s %s cloudflare unavailable status %d: %s", service, action, statusCode, snippet)
		}
		return fmt.Errorf("%s %s cloudflare unavailable status %d", service, action, statusCode)
	case 524:
		if snippet != "" {
			return fmt.Errorf("%s %s cloudflare timeout status %d: %s", service, action, statusCode, snippet)
		}
		return fmt.Errorf("%s %s cloudflare timeout status %d", service, action, statusCode)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		if snippet != "" {
			return fmt.Errorf("%s %s status %d: %s", service, action, statusCode, snippet)
		}
	}
	if snippet != "" {
		return fmt.Errorf("%s %s status %d: %s", service, action, statusCode, snippet)
	}
	return fmt.Errorf("%s %s status %d", service, action, statusCode)
}

// DetectResponseError sniffs a 2xx (or not-yet-classified) response body for
// Cloudflare/gateway HTML error pages that some proxies return with a
// misleadingly successful status code. Returns nil if nothing suspicious is
// found.
func DetectResponseError(service, action string, statusCode int, body []byte) error {
	text := strings.ToLower(strings.TrimSpace(string(body)))
	switch {
	case strings.Contains(text, "cloudflare") && strings.Contains(text, "524"):
		return fmt.Errorf("%s %s cloudflare timeout status %d", service, action, statusCode)
	case strings.Contains(text, "cloudflare") && strings.Contains(text, "522"):
		return fmt.Errorf("%s %s cloudflare unavailable status %d", service, action, statusCode)
	case strings.Contains(text, "cloudflare") && strings.Contains(text, "timed out"):
		return fmt.Errorf("%s %s cloudflare timeout status %d", service, action, statusCode)
	case strings.Contains(text, "<html") && strings.Contains(text, "cloudflare"):
		return fmt.Errorf("%s %s cloudflare unavailable status %d", service, action, statusCode)
	case strings.Contains(text, "<html") && strings.Contains(text, "bad gateway"):
		return fmt.Errorf("%s %s status %d: bad gateway", service, action, statusCode)
	case strings.Contains(text, "<html") && strings.Contains(text, "gateway timeout"):
		return fmt.Errorf("%s %s status %d: gateway timeout", service, action, statusCode)
	default:
		return nil
	}
}

// SummarizeBody collapses a response body to a single-line, 160-char
// snippet suitable for an error message.
func SummarizeBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 160 {
		text = text[:160]
	}
	return text
}
