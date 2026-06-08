package archive

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hjongedijk/drakkar/internal/database"
)

func DetectImportedArchives(files []database.ImportedNZBFile) []database.ImportedArchive {
	groups := map[string][]database.ImportedArchiveVolume{}
	for _, file := range files {
		groupKey, volume, ok := detectRARVolume(file.FileName)
		if !ok {
			continue
		}
		groups[groupKey] = append(groups[groupKey], volume)
	}
	if len(groups) == 0 {
		return nil
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]database.ImportedArchive, 0, len(keys))
	for _, key := range keys {
		volumes := groups[key]
		sort.Slice(volumes, func(i, j int) bool {
			if volumes[i].VolumeIndex == volumes[j].VolumeIndex {
				return volumes[i].Path < volumes[j].Path
			}
			return volumes[i].VolumeIndex < volumes[j].VolumeIndex
		})
		out = append(out, database.ImportedArchive{
			Kind:    "rar",
			Status:  "pending",
			Volumes: volumes,
		})
	}
	return out
}

func detectRARVolume(name string) (string, database.ImportedArchiveVolume, bool) {
	base := filepath.Base(strings.TrimSpace(name))
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".part01.rar") || strings.Contains(lower, ".part") && strings.HasSuffix(lower, ".rar") {
		idx := strings.LastIndex(lower, ".part")
		if idx < 0 || idx+5 >= len(lower) {
			return "", database.ImportedArchiveVolume{}, false
		}
		numberPart := lower[idx+5 : len(lower)-4]
		number, err := strconv.Atoi(numberPart)
		if err != nil || number <= 0 {
			return "", database.ImportedArchiveVolume{}, false
		}
		return base[:idx], database.ImportedArchiveVolume{Path: base, VolumeIndex: number - 1}, true
	}
	if strings.HasSuffix(lower, ".rar") {
		return strings.TrimSuffix(base, filepath.Ext(base)), database.ImportedArchiveVolume{Path: base, VolumeIndex: 0}, true
	}
	ext := filepath.Ext(lower)
	if len(ext) == 4 && strings.HasPrefix(ext, ".r") {
		number, err := strconv.Atoi(ext[2:])
		if err != nil {
			return "", database.ImportedArchiveVolume{}, false
		}
		return strings.TrimSuffix(base, filepath.Ext(base)), database.ImportedArchiveVolume{Path: base, VolumeIndex: number + 1}, true
	}
	return "", database.ImportedArchiveVolume{}, false
}
