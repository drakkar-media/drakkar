package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	plexPinsURL  = "https://plex.tv/api/v2/pins"
	plexAuthBase = "https://app.plex.tv/auth"
	plexProduct  = "Drakkar"
	plexClientID = "drakkar"
)

var oauthHTTPClient = &http.Client{Timeout: 15 * time.Second}

// OAuthPin represents a Plex PIN-based login request. AuthURL is the page the
// user must open to grant access; PinID is then passed to PollOAuth to check
// whether that grant has completed.
type OAuthPin struct {
	PinID            int64  `json:"pinId"`
	Code             string `json:"code"`
	AuthURL          string `json:"authUrl"`
	ClientIdentifier string `json:"clientIdentifier"`
}

// OAuthPoll is the result of checking a pending PIN. Token is only populated
// once Authorized is true.
type OAuthPoll struct {
	Authorized bool   `json:"authorized"`
	Token      string `json:"token,omitempty"`
}

// StartOAuth begins the Plex PIN-based OAuth flow by requesting a new PIN from
// the Plex API and building the auth URL the end user must visit to authorize
// this application.
//
// The returned OAuthPin.PinID must be polled via PollOAuth until authorized;
// Plex PINs expire after a short window if the user never completes the flow.
func StartOAuth(ctx context.Context) (OAuthPin, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, plexPinsURL+"?strong=true", nil)
	if err != nil {
		return OAuthPin{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Client-Identifier", plexClientID)
	req.Header.Set("X-Plex-Product", plexProduct)

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return OAuthPin{}, fmt.Errorf("plex PIN request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return OAuthPin{}, fmt.Errorf("plex API HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return OAuthPin{}, err
	}

	var pin struct {
		ID   int64  `json:"id"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &pin); err != nil {
		return OAuthPin{}, fmt.Errorf("parse plex PIN: %w", err)
	}

	params := url.Values{}
	params.Set("clientID", plexClientID)
	params.Set("code", pin.Code)
	params.Set("context[device][product]", plexProduct)
	authURL := plexAuthBase + "#?" + params.Encode()

	return OAuthPin{
		PinID:            pin.ID,
		Code:             pin.Code,
		AuthURL:          authURL,
		ClientIdentifier: plexClientID,
	}, nil
}

// PollOAuth checks whether the user has authorized the PIN identified by
// pinID. Callers are expected to poll this repeatedly (Plex has no push
// notification for PIN completion) until Authorized is true or the PIN
// expires on Plex's side.
func PollOAuth(ctx context.Context, pinID int64) (OAuthPoll, error) {
	endpoint := fmt.Sprintf("%s/%d", plexPinsURL, pinID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return OAuthPoll{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Client-Identifier", plexClientID)
	req.Header.Set("X-Plex-Product", plexProduct)

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return OAuthPoll{}, fmt.Errorf("plex poll request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return OAuthPoll{}, fmt.Errorf("plex API HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return OAuthPoll{}, err
	}

	var result struct {
		AuthToken string `json:"authToken"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return OAuthPoll{}, fmt.Errorf("parse plex poll: %w", err)
	}

	if result.AuthToken != "" {
		return OAuthPoll{Authorized: true, Token: result.AuthToken}, nil
	}
	return OAuthPoll{Authorized: false}, nil
}
