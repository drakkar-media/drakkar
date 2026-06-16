package database

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DetectImportedArchives groups NZB files by archive membership, returning one
// ImportedArchive per RAR/7z set found. Archives start in "pending" status
// until inspectImportedArchives upgrades them to "supported" or "rejected".
func DetectImportedArchives(files []ImportedNZBFile) []ImportedArchive {
	type archiveGroup struct {
		kind    string
		volumes []ImportedArchiveVolume
	}
	groups := map[string]archiveGroup{}
	for _, file := range files {
		groupKey, kind, volume, ok := detectArchiveVolume(file.FileName)
		if !ok {
			continue
		}
		group := groups[groupKey]
		if group.kind == "" {
			group.kind = kind
		}
		group.volumes = append(group.volumes, volume)
		groups[groupKey] = group
	}
	if len(groups) == 0 {
		return nil
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]ImportedArchive, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		volumes := group.volumes
		sort.Slice(volumes, func(i, j int) bool {
			if volumes[i].VolumeIndex == volumes[j].VolumeIndex {
				return volumes[i].Path < volumes[j].Path
			}
			return volumes[i].VolumeIndex < volumes[j].VolumeIndex
		})
		out = append(out, ImportedArchive{
			Kind:    group.kind,
			Status:  "pending",
			Volumes: volumes,
		})
	}
	return out
}

func detectArchiveVolume(name string) (string, string, ImportedArchiveVolume, bool) {
	if key, volume, ok := detectRARVolume(name); ok {
		return key, "rar", volume, true
	}
	if key, volume, ok := detect7zVolume(name); ok {
		return key, "7z", volume, true
	}
	return "", "", ImportedArchiveVolume{}, false
}

func detectRARVolume(name string) (string, ImportedArchiveVolume, bool) {
	base := filepath.Base(strings.TrimSpace(name))
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".part01.rar") || strings.Contains(lower, ".part") && strings.HasSuffix(lower, ".rar") {
		idx := strings.LastIndex(lower, ".part")
		if idx < 0 || idx+5 >= len(lower) {
			return "", ImportedArchiveVolume{}, false
		}
		numberPart := lower[idx+5 : len(lower)-4]
		number, err := strconv.Atoi(numberPart)
		if err != nil || number <= 0 {
			return "", ImportedArchiveVolume{}, false
		}
		return base[:idx], ImportedArchiveVolume{Path: base, VolumeIndex: number - 1}, true
	}
	if strings.HasSuffix(lower, ".rar") {
		return strings.TrimSuffix(base, filepath.Ext(base)), ImportedArchiveVolume{Path: base, VolumeIndex: 0}, true
	}
	ext := filepath.Ext(lower)
	if len(ext) == 4 && strings.HasPrefix(ext, ".r") {
		number, err := strconv.Atoi(ext[2:])
		if err != nil {
			return "", ImportedArchiveVolume{}, false
		}
		return strings.TrimSuffix(base, filepath.Ext(base)), ImportedArchiveVolume{Path: base, VolumeIndex: number + 1}, true
	}
	return "", ImportedArchiveVolume{}, false
}

func detect7zVolume(name string) (string, ImportedArchiveVolume, bool) {
	base := filepath.Base(strings.TrimSpace(name))
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".7z") {
		return strings.TrimSuffix(base, filepath.Ext(base)), ImportedArchiveVolume{Path: base, VolumeIndex: 0}, true
	}
	idx := strings.LastIndex(lower, ".7z.")
	if idx < 0 {
		return "", ImportedArchiveVolume{}, false
	}
	part := lower[idx+4:]
	number, err := strconv.Atoi(strings.TrimPrefix(part, "."))
	if err != nil || number <= 0 {
		return "", ImportedArchiveVolume{}, false
	}
	return base[:idx], ImportedArchiveVolume{Path: base, VolumeIndex: number - 1}, true
}
