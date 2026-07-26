package seerr

// WebhookPayload is the JSON body Seerr sends to configured webhook URLs.
// Only fields relevant to Drakkar are mapped; unknown fields are ignored.
type WebhookPayload struct {
	NotificationType string          `json:"notification_type"`
	Subject          string          `json:"subject"`
	Media            *WebhookMedia   `json:"media"`
	Request          *WebhookRequest `json:"request"`
}

// WebhookMedia identifies the media item a webhook notification concerns.
type WebhookMedia struct {
	MediaType string `json:"media_type"` // "movie" or "tv"
	TMDbID    string `json:"tmdbId"`
	TVDbID    string `json:"tvdbId"`
	IMDbID    string `json:"imdbId"`
	Status    string `json:"status"`
}

// WebhookRequest identifies the Seerr request a webhook notification
// concerns.
type WebhookRequest struct {
	RequestID string `json:"request_id"`
}

// actionableNotifications lists every Seerr notification type Drakkar knows
// about and whether it should trigger a request sync. The false entries are
// listed explicitly (rather than relying on the map's zero-value default)
// so every known notification type is visibly accounted for; an unlisted
// type not covered by this map also resolves to false via IsActionable.
var actionableNotifications = map[string]bool{
	"MEDIA_APPROVED":      true,
	"MEDIA_PENDING":       true,
	"MEDIA_AUTO_APPROVED": true,
	"MEDIA_AVAILABLE":     true,
	"MEDIA_DECLINED":      false,
	"MEDIA_FAILED":        false,
	"TEST_NOTIFICATION":   false,
	"ISSUE_CREATED":       false,
	"ISSUE_COMMENT":       false,
	"ISSUE_RESOLVED":      false,
	"ISSUE_REOPENED":      false,
}

// IsActionable returns true when Drakkar should react to this notification
// by syncing requests from Seerr.
func (p *WebhookPayload) IsActionable() bool {
	return actionableNotifications[p.NotificationType]
}
