package subdl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/subtitles"
	"github.com/drakkar-media/drakkar/internal/subtitleutil"
)

type Client struct {
	apiKey      string
	baseURL     string
	downloadURL string
	httpClient  *http.Client
}

func NewClient(auth config.SubtitleAuth) *Client {
	return &Client{
		apiKey:      strings.TrimSpace(auth.APIKey),
		baseURL:     "https://api.subdl.com/api/v1",
		downloadURL: "https://dl.subdl.com",
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Name() string {
	return "subdl"
}

func (c *Client) Probe(ctx context.Context) error {
	_, err := c.Search(ctx, database.SubtitleSearchInput{
		MediaType: "movie",
		Title:     "Drakkar",
	}, []string{"en"})
	return err
}

func (c *Client) Search(ctx context.Context, input database.SubtitleSearchInput, languages []string) ([]subtitles.ProviderCandidate, error) {
	u, err := url.Parse(c.baseURL + "/subtitles")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("api_key", c.apiKey)
	if input.TMDBID > 0 {
		q.Set("tmdb_id", fmt.Sprintf("%d", input.TMDBID))
	} else {
		q.Set("film_name", subtitleutil.SearchTitle(input))
	}
	if value := typeForSearch(input.MediaType); value != "" {
		q.Set("type", value)
	}
	if year := subtitleutil.SearchYear(input); year > 0 {
		q.Set("year", fmt.Sprintf("%d", year))
	}
	if input.SeasonNumber > 0 {
		q.Set("season_number", fmt.Sprintf("%d", input.SeasonNumber))
	}
	if input.EpisodeNumber > 0 {
		q.Set("episode_number", fmt.Sprintf("%d", input.EpisodeNumber))
	}
	if joined := normalizeLanguages(languages); joined != "" {
		q.Set("languages", joined)
	}
	q.Set("subs_per_page", "30")
	q.Set("unpack", "1")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subdl search status %d", resp.StatusCode)
	}

	var payload struct {
		Status    bool `json:"status"`
		Subtitles []struct {
			Name        string `json:"name"`
			ReleaseName string `json:"release_name"`
			Season      int    `json:"season"`
			Episode     int    `json:"episode"`
			Language    string `json:"language"`
			HI          bool   `json:"hi"`
			Format      string `json:"format"`
			URL         string `json:"url"`
			UnpackFiles []struct {
				FileID      string `json:"file_n_id"`
				Name        string `json:"name"`
				ReleaseName string `json:"release_name"`
				Season      int    `json:"season"`
				Episode     int    `json:"episode"`
				Language    string `json:"language"`
				HI          bool   `json:"hi"`
				Format      string `json:"format"`
				URL         string `json:"url"`
			} `json:"unpack_files"`
		} `json:"subtitles"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if !payload.Status && payload.Error != "" {
		return nil, fmt.Errorf("subdl search failed: %s", payload.Error)
	}

	var out []subtitles.ProviderCandidate
	for _, item := range payload.Subtitles {
		if len(item.UnpackFiles) == 0 {
			format := strings.ToLower(strings.TrimSpace(item.Format))
			if format == "zip" || format == "srt" || format == "vtt" {
				out = append(out, subtitles.ProviderCandidate{
					Language:        strings.ToLower(strings.TrimSpace(item.Language)),
					Title:           subtitleutil.FirstNonEmpty(item.Name, item.ReleaseName),
					ReleaseName:     subtitleutil.FirstNonEmpty(item.ReleaseName, item.Name),
					Format:          format,
					HearingImpaired: item.HI,
					ExternalID:      subtitleutil.FirstNonEmpty(item.URL, item.Name),
					DownloadURL:     c.downloadURL + item.URL,
					SeasonNumber:    item.Season,
					EpisodeNumber:   item.Episode,
				})
			}
		}
		for _, unpack := range item.UnpackFiles {
			format := strings.ToLower(strings.TrimSpace(unpack.Format))
			if format != "srt" && format != "vtt" {
				continue
			}
			out = append(out, subtitles.ProviderCandidate{
				Language:        strings.ToLower(strings.TrimSpace(unpack.Language)),
				Title:           subtitleutil.FirstNonEmpty(unpack.Name, item.Name),
				ReleaseName:     subtitleutil.FirstNonEmpty(unpack.ReleaseName, item.ReleaseName, unpack.Name),
				Format:          format,
				HearingImpaired: unpack.HI,
				ExternalID:      unpack.FileID,
				DownloadURL:     c.downloadURL + unpack.URL,
				SeasonNumber:    unpack.Season,
				EpisodeNumber:   unpack.Episode,
			})
		}
	}
	return out, nil
}

func (c *Client) Download(ctx context.Context, rawURL string) (string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("subdl download status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if err != nil {
		return "", nil, err
	}
	name := path.Base(req.URL.Path)
	if name == "." || name == "/" || name == "" {
		name = "subtitle.srt"
	}
	return name, body, nil
}

func normalizeLanguages(values []string) string {
	var out []string
	seen := make(map[string]struct{})
	for _, value := range values {
		normalized := strings.ToUpper(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return strings.Join(out, ",")
}

func typeForSearch(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		return "movie"
	case "episode", "tv":
		return "tv"
	default:
		return ""
	}
}
