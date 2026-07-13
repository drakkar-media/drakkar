// Package subtitleutil holds search-input helpers shared by the subtitle
// providers (opensubtitles, subdl) and the orchestrating subtitles service.
// Provider-specific formatting (e.g. language-code casing, media-type
// vocabulary) stays in each client — only the identical logic is here.
package subtitleutil

import (
	"strings"

	"github.com/drakkar-media/drakkar/internal/database"
)

// SearchTitle picks the show title for episode/tv searches (falling back to
// Title if ShowTitle is unset) or the movie title otherwise.
func SearchTitle(input database.SubtitleSearchInput) string {
	if strings.EqualFold(input.MediaType, "episode") || strings.EqualFold(input.MediaType, "tv") {
		if strings.TrimSpace(input.ShowTitle) != "" {
			return input.ShowTitle
		}
	}
	return input.Title
}

// SearchYear picks the movie or show release year depending on media type.
func SearchYear(input database.SubtitleSearchInput) int {
	if strings.EqualFold(input.MediaType, "movie") {
		return input.MovieYear
	}
	return input.ShowYear
}

// FirstNonEmpty returns the first non-blank (after trimming) value, or "".
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
