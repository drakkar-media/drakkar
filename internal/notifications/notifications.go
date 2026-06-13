// Package notifications sends event notifications to external services
// (Discord webhooks, generic webhooks) when media items are grabbed,
// become available, or permanently fail.
package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// EventType classifies what happened.
type EventType string

const (
	EventGrab      EventType = "grab"
	EventAvailable EventType = "available"
	EventFailed    EventType = "failed"
)

// Event carries the data for a notification.
type Event struct {
	Type        EventType
	Title       string
	MediaType   string // "movie" or "episode"
	Resolution  string
	Indexer     string
	Score       int
	FailReason  string
}

// Config holds per-provider notification settings.
type Config struct {
	// DiscordWebhookURL sends a Discord embed on grab/available/failed.
	DiscordWebhookURL string `json:"discordWebhookUrl"`
	// GenericWebhookURL receives a JSON POST body for every event.
	GenericWebhookURL string `json:"genericWebhookUrl"`
	// OnGrab: notify when a release is selected for download.
	OnGrab bool `json:"onGrab"`
	// OnAvailable: notify when an item finishes importing.
	OnAvailable bool `json:"onAvailable"`
	// OnFailed: notify when an item permanently fails.
	OnFailed bool `json:"onFailed"`
}

// Notifier dispatches notifications according to Config.
type Notifier struct {
	cfg    Config
	client *http.Client
	log    *slog.Logger
}

func New(cfg Config, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		log:    log,
	}
}

// Send dispatches an event to all configured providers.
// Errors are logged but not returned — notifications are best-effort.
func (n *Notifier) Send(ctx context.Context, ev Event) {
	if !n.shouldSend(ev.Type) {
		return
	}
	if n.cfg.DiscordWebhookURL != "" {
		if err := n.sendDiscord(ctx, ev); err != nil {
			n.log.Warn("discord notification failed", "error", err, "event", ev.Type)
		}
	}
	if n.cfg.GenericWebhookURL != "" {
		if err := n.sendGeneric(ctx, ev); err != nil {
			n.log.Warn("webhook notification failed", "error", err, "event", ev.Type)
		}
	}
}

func (n *Notifier) shouldSend(t EventType) bool {
	switch t {
	case EventGrab:
		return n.cfg.OnGrab
	case EventAvailable:
		return n.cfg.OnAvailable
	case EventFailed:
		return n.cfg.OnFailed
	}
	return false
}

// ── Discord ──────────────────────────────────────────────────────────────────

type discordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordPayload struct {
	Username string         `json:"username"`
	Embeds   []discordEmbed `json:"embeds"`
}

func (n *Notifier) sendDiscord(ctx context.Context, ev Event) error {
	color, label := eventMeta(ev.Type)
	embed := discordEmbed{
		Title: fmt.Sprintf("%s – %s", label, ev.Title),
		Color: color,
	}
	if ev.Resolution != "" {
		embed.Fields = append(embed.Fields, discordEmbedField{Name: "Quality", Value: ev.Resolution, Inline: true})
	}
	if ev.Indexer != "" {
		embed.Fields = append(embed.Fields, discordEmbedField{Name: "Indexer", Value: ev.Indexer, Inline: true})
	}
	if ev.FailReason != "" {
		embed.Fields = append(embed.Fields, discordEmbedField{Name: "Reason", Value: ev.FailReason, Inline: false})
	}
	payload := discordPayload{Username: "Drakkar", Embeds: []discordEmbed{embed}}
	return n.post(ctx, n.cfg.DiscordWebhookURL, payload)
}

// ── Generic webhook ──────────────────────────────────────────────────────────

type genericPayload struct {
	EventType  string `json:"eventType"`
	Title      string `json:"title"`
	MediaType  string `json:"mediaType"`
	Resolution string `json:"resolution,omitempty"`
	Indexer    string `json:"indexer,omitempty"`
	Score      int    `json:"score,omitempty"`
	FailReason string `json:"failReason,omitempty"`
}

func (n *Notifier) sendGeneric(ctx context.Context, ev Event) error {
	payload := genericPayload{
		EventType:  string(ev.Type),
		Title:      ev.Title,
		MediaType:  ev.MediaType,
		Resolution: ev.Resolution,
		Indexer:    ev.Indexer,
		Score:      ev.Score,
		FailReason: ev.FailReason,
	}
	return n.post(ctx, n.cfg.GenericWebhookURL, payload)
}

// ── HTTP helper ──────────────────────────────────────────────────────────────

func (n *Notifier) post(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return nil
}

func eventMeta(t EventType) (color int, label string) {
	switch t {
	case EventGrab:
		return 0x3498DB, "Grabbed" // blue
	case EventAvailable:
		return 0x2ECC71, "Available" // green
	case EventFailed:
		return 0xE74C3C, "Failed" // red
	}
	return 0x95A5A6, string(t)
}
