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

type OAuthPin struct {
	PinID            int64  `json:"pinId"`
	Code             string `json:"code"`
	AuthURL          string `json:"authUrl"`
	ClientIdentifier string `json:"clientIdentifier"`
}

type OAuthPoll struct {
	Authorized bool   `json:"authorized"`
	Token      string `json:"token,omitempty"`
}

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
