// Package symlink builds Plex/Jellyfin-style library paths for movies and
// episodes and publishes media into them via symlinks, keeping the
// publication step atomic and idempotent with respect to the already-linked
// target.
package symlink

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mediaExt extracts the file extension from a raw NZB filename (e.g. ".mkv").
// Returns ".mkv" as the fallback when nothing is recognised.
func mediaExt(rawFileName string) string {
	ext := strings.ToLower(filepath.Ext(rawFileName))
	switch ext {
	case ".mkv", ".mp4", ".avi", ".mov", ".ts", ".m4v":
		return ext
	}
	return ".mkv"
}

// Publisher creates the symlinks that expose downloaded media under a
// library-friendly path. Publisher holds no state and is safe for
// concurrent use.
type Publisher struct{}

// NewPublisher creates a Publisher.
func NewPublisher() *Publisher {
	return &Publisher{}
}

// Publish ensures a symlink exists at finalPath pointing to target.
//
// It is a no-op when finalPath already links to target. Otherwise the link
// is created at a temporary sibling path and atomically renamed into place,
// so a concurrent reader (e.g. a media server scanning the library) never
// observes a missing or partially created link, and a crash mid-publish
// cannot leave finalPath pointing at a stale target.
func (p *Publisher) Publish(finalPath, target string) error {
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return err
	}
	if existing, err := os.Readlink(finalPath); err == nil && existing == target {
		return nil
	}
	tmp := finalPath + ".tmp-drakkar"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// MoviePath builds the publication path for a movie, following the
// "Title (Year) {tmdb-ID}/Title (Year).ext" layout that Plex/Jellyfin use to
// disambiguate movies sharing a title. tmdbID and year are omitted from the
// directory/file name when not available (tmdbID <= 0 or year <= 0).
func MoviePath(root, title string, year int, tmdbID int, rawFileName string) string {
	ext := mediaExt(rawFileName)
	var dir, file string
	if tmdbID > 0 {
		dir = fmt.Sprintf("%s (%d) {tmdb-%d}", sanitize(title), year, tmdbID)
	} else if year > 0 {
		dir = fmt.Sprintf("%s (%d)", sanitize(title), year)
	} else {
		dir = sanitize(title)
	}
	if year > 0 {
		file = fmt.Sprintf("%s (%d)%s", sanitize(title), year, ext)
	} else {
		file = fmt.Sprintf("%s%s", sanitize(title), ext)
	}
	return filepath.Join(root, dir, file)
}

// EpisodePath builds the publication path for a TV episode, following the
// "Show (Year) {tvdb-ID}/Season NN/Show - SNNENN.ext" layout that
// Plex/Jellyfin use for TV libraries. tvdbID and year are omitted from the
// show directory name when not available (tvdbID <= 0 or year <= 0).
func EpisodePath(root, show string, year int, tvdbID int, season, episode int, rawFileName string) string {
	ext := mediaExt(rawFileName)
	var dir string
	if tvdbID > 0 {
		dir = fmt.Sprintf("%s (%d) {tvdb-%d}", sanitize(show), year, tvdbID)
	} else if year > 0 {
		dir = fmt.Sprintf("%s (%d)", sanitize(show), year)
	} else {
		dir = sanitize(show)
	}
	file := fmt.Sprintf("%s - S%02dE%02d%s", sanitize(show), season, episode, ext)
	return filepath.Join(root, dir, fmt.Sprintf("Season %02d", season), file)
}

// sanitize replaces path-hostile characters in a title/show name so it can
// be used as a filesystem path component.
func sanitize(input string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", " -")
	out := replacer.Replace(strings.TrimSpace(input))
	// A result of "." or ".." (or empty, from blank/whitespace-only metadata)
	// would resolve to the library root or its parent when joined into a
	// path — filepath.Join does not stop a ".." segment from escaping root.
	if out == "" || out == "." || out == ".." || strings.Trim(out, ".") == "" {
		return "_"
	}
	return out
}
