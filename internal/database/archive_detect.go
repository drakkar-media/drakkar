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

// detectArchiveVolume tries each known archive naming convention in turn and
// reports the archive group key, kind, and volume descriptor for the first
// one that matches. Returns ok=false for filenames that aren't part of any
// recognized RAR/7z volume set.
func detectArchiveVolume(name string) (string, string, ImportedArchiveVolume, bool) {
	if key, volume, ok := detectRARVolume(name); ok {
		return key, "rar", volume, true
	}
	if key, volume, ok := detect7zVolume(name); ok {
		return key, "7z", volume, true
	}
	return "", "", ImportedArchiveVolume{}, false
}

// detectRARVolume recognizes the three RAR volume naming conventions in use:
// modern "*.partNN.rar" (1-indexed in the filename, stored 0-indexed),
// a lone "*.rar" (single-volume archive, index 0), and legacy "*.rNN"
// continuation volumes. Legacy numbering is offset by +1 (VolumeIndex =
// number+1) because the first volume of that scheme is the separate ".rar"
// file (index 0); ".r00" is the second volume (index 1), ".r01" the third,
// and so on -- the archive group key returned is the shared basename all
// volumes of the set sort under.
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

// detect7zVolume recognizes a lone "*.7z" (single-volume archive, index 0) or
// a "*.7z.NNN" split-volume continuation part (1-indexed in the filename,
// stored 0-indexed), returning the shared basename as the archive group key.
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
