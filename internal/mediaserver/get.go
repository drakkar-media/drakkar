// Package mediaserver holds the HTTP GET helper shared by the Jellyfin and
// Plex clients — both did an identical build-request/set-one-auth-header/
// check-status/decode-JSON dance, differing only in header name and error
// message prefix.
package mediaserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Get performs an authenticated GET against a media server's REST API and
// decodes the JSON response into out (skipped if out is nil).
// service names the server in error messages (e.g. "jellyfin", "plex").
func Get(ctx context.Context, httpClient *http.Client, serverURL, path, authHeader, authValue, service string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set(authHeader, authValue)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s HTTP %d", service, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
