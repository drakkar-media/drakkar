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

type Publisher struct{}

func NewPublisher() *Publisher {
	return &Publisher{}
}

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

func sanitize(input string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", " -")
	return replacer.Replace(strings.TrimSpace(input))
}
