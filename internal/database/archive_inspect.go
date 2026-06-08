package database

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hjongedijk/drakkar/internal/stream"
)

const inspectHeaderLimit = 256 * 1024

var (
	errArchiveHeadersInvalid         = errors.New("archive_headers_invalid")
	errArchiveCompressionUnsupported = errors.New("archive_compression_unsupported")
	errArchiveSolidUnsupported       = errors.New("archive_solid_unsupported")
	errArchiveEncrypted              = errors.New("archive_encrypted")
	errArchiveVideoNotFound          = errors.New("archive_video_not_found")
)

func inspectImportedArchives(ctx context.Context, archives []ImportedArchive, files []ImportedNZBFile, fetcher stream.SegmentFetcher) []ImportedArchive {
	if len(archives) == 0 {
		return nil
	}
	fileByName := make(map[string]ImportedNZBFile, len(files))
	for _, file := range files {
		fileByName[file.FileName] = file
	}
	out := make([]ImportedArchive, 0, len(archives))
	for _, item := range archives {
		if fetcher == nil {
			if item.Status == "" {
				item.Status = "pending"
			}
			out = append(out, item)
			continue
		}
		inspected := item
		if err := inspectArchive(ctx, &inspected, fileByName, fetcher); err != nil {
			inspected.Status = "rejected"
			inspected.RejectReason = err.Error()
		}
		out = append(out, inspected)
	}
	return out
}

func inspectArchive(ctx context.Context, archive *ImportedArchive, fileByName map[string]ImportedNZBFile, fetcher stream.SegmentFetcher) error {
	if archive == nil {
		return nil
	}
	if archive.Kind != "rar" {
		return errArchiveHeadersInvalid
	}
	if !hasContiguousVolumes(archive.Volumes) {
		return errArchiveHeadersInvalid
	}
	if len(archive.Volumes) == 0 {
		return errArchiveHeadersInvalid
	}
	first, ok := fileByName[archive.Volumes[0].Path]
	if !ok {
		return errArchiveHeadersInvalid
	}
	volumeSizes := make(map[int]int64, len(archive.Volumes))
	for _, volume := range archive.Volumes {
		file, ok := fileByName[volume.Path]
		if !ok {
			return errArchiveHeadersInvalid
		}
		volumeSizes[volume.VolumeIndex] = file.FileSizeBytes
	}
	prefix, err := readImportedFilePrefix(ctx, first, inspectHeaderLimit, fetcher)
	if err != nil {
		return fmt.Errorf("%w", errArchiveHeadersInvalid)
	}
	entries, err := inspectRAR4(prefix)
	if err != nil {
		return err
	}
	assignArchiveRanges(entries, volumeSizes)
	if err := validatePlayableArchiveEntries(entries); err != nil {
		return err
	}
	archive.Entries = entries
	archive.Status = "supported"
	archive.RejectReason = ""
	return nil
}

func readImportedFilePrefix(ctx context.Context, file ImportedNZBFile, limit int64, fetcher stream.SegmentFetcher) ([]byte, error) {
	if limit <= 0 || file.FileSizeBytes <= 0 {
		return nil, errors.New("invalid archive size")
	}
	if limit > file.FileSizeBytes {
		limit = file.FileSizeBytes
	}
	spans := make([]stream.SegmentSpan, 0, len(file.Segments))
	for _, segment := range file.Segments {
		spans = append(spans, stream.SegmentSpan{
			MessageID: segment.MessageID,
			Start:     segment.DecodedStartOffset,
			End:       segment.DecodedEndOffset,
		})
	}
	ranges, err := stream.ResolveRange(spans, 0, limit)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, limit)
	for _, item := range ranges {
		block, err := fetcher.FetchRange(ctx, item)
		if err != nil {
			return nil, err
		}
		out = append(out, block...)
		if int64(len(out)) >= limit {
			return out[:limit], nil
		}
	}
	if int64(len(out)) < limit {
		return nil, errors.New("short archive header fetch")
	}
	return out[:limit], nil
}

func inspectRAR4(raw []byte) ([]ImportedArchiveEntry, error) {
	if len(raw) < 13 || string(raw[:7]) != "Rar!\x1a\x07\x00" {
		return nil, errArchiveHeadersInvalid
	}
	offset := 7
	var (
		mainFlags     uint16
		entries       []ImportedArchiveEntry
		playableFound bool
	)
	for offset+7 <= len(raw) {
		headType := raw[offset+2]
		headFlags := binary.LittleEndian.Uint16(raw[offset+3 : offset+5])
		headSize := int(binary.LittleEndian.Uint16(raw[offset+5 : offset+7]))
		if headSize < 7 || offset+headSize > len(raw) {
			return nil, errArchiveHeadersInvalid
		}
		body := raw[offset+7 : offset+headSize]
		switch headType {
		case 0x73:
			mainFlags = headFlags
		case 0x74:
			entry, packedSize, err := parseRAR4FileHeader(body, headFlags, mainFlags, int64(offset+headSize))
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
			if isPlayableArchiveEntry(entry.Path) {
				playableFound = true
				if entry.Encrypted {
					return nil, errArchiveEncrypted
				}
				if entry.Solid {
					return nil, errArchiveSolidUnsupported
				}
				if entry.CompressionMethod != "m0" {
					return nil, errArchiveCompressionUnsupported
				}
			}
			offset += headSize + int(packedSize)
			continue
		case 0x7b:
			offset = len(raw)
			continue
		}
		offset += headSize
	}
	if len(entries) == 0 {
		return nil, errArchiveHeadersInvalid
	}
	if !playableFound {
		return entries, errArchiveVideoNotFound
	}
	return entries, nil
}

func parseRAR4FileHeader(body []byte, headFlags, mainFlags uint16, dataOffset int64) (ImportedArchiveEntry, uint32, error) {
	if len(body) < 25 {
		return ImportedArchiveEntry{}, 0, errArchiveHeadersInvalid
	}
	packedSize := uint64(binary.LittleEndian.Uint32(body[0:4]))
	unpackedSize := uint64(binary.LittleEndian.Uint32(body[4:8]))
	method := body[18]
	nameSize := int(binary.LittleEndian.Uint16(body[19:21]))
	pos := 25
	if headFlags&0x0100 != 0 {
		if len(body) < pos+8 {
			return ImportedArchiveEntry{}, 0, errArchiveHeadersInvalid
		}
		highPacked := uint64(binary.LittleEndian.Uint32(body[pos : pos+4]))
		highUnpacked := uint64(binary.LittleEndian.Uint32(body[pos+4 : pos+8]))
		packedSize |= highPacked << 32
		unpackedSize |= highUnpacked << 32
		pos += 8
	}
	if len(body) < pos+nameSize {
		return ImportedArchiveEntry{}, 0, errArchiveHeadersInvalid
	}
	name := string(body[pos : pos+nameSize])
	return ImportedArchiveEntry{
		Path:              filepath.Base(strings.ReplaceAll(name, `\`, "/")),
		SizeBytes:         int64(unpackedSize),
		PackedSizeBytes:   int64(packedSize),
		CompressionMethod: rarMethodName(method),
		Encrypted:         headFlags&0x0004 != 0 || mainFlags&0x0080 != 0,
		Solid:             mainFlags&0x0008 != 0,
		VolumeIndex:       0,
		ArchiveOffset:     dataOffset,
	}, uint32(packedSize), nil
}

func assignArchiveRanges(entries []ImportedArchiveEntry, volumeSizes map[int]int64) {
	for i := range entries {
		entry := &entries[i]
		if entry.PackedSizeBytes <= 0 || entry.VolumeIndex < 0 {
			continue
		}
		remaining := entry.PackedSizeBytes
		entryOffset := int64(0)
		volumeIndex := entry.VolumeIndex
		archiveOffset := entry.ArchiveOffset
		for remaining > 0 {
			volumeSize, ok := volumeSizes[volumeIndex]
			if !ok || archiveOffset >= volumeSize {
				entry.Ranges = nil
				break
			}
			available := volumeSize - archiveOffset
			length := remaining
			if length > available {
				length = available
			}
			entry.Ranges = append(entry.Ranges, ImportedArchiveRange{
				VolumeIndex:   volumeIndex,
				EntryOffset:   entryOffset,
				ArchiveOffset: archiveOffset,
				LengthBytes:   length,
			})
			remaining -= length
			entryOffset += length
			volumeIndex++
			archiveOffset = 0
		}
		if remaining > 0 {
			entry.Ranges = nil
		}
	}
}

func validatePlayableArchiveEntries(entries []ImportedArchiveEntry) error {
	for _, entry := range entries {
		if !isPlayableArchiveEntry(entry.Path) {
			continue
		}
		if !hasCompleteArchiveMapping(entry) {
			return errArchiveHeadersInvalid
		}
	}
	return nil
}

func hasCompleteArchiveMapping(entry ImportedArchiveEntry) bool {
	if entry.PackedSizeBytes < 0 {
		return false
	}
	if entry.PackedSizeBytes == 0 {
		return len(entry.Ranges) == 0
	}
	if len(entry.Ranges) == 0 {
		return false
	}
	expectedOffset := int64(0)
	var total int64
	for _, item := range entry.Ranges {
		if item.EntryOffset != expectedOffset || item.LengthBytes <= 0 {
			return false
		}
		expectedOffset += item.LengthBytes
		total += item.LengthBytes
	}
	return total == entry.PackedSizeBytes
}

func rarMethodName(method byte) string {
	switch method {
	case 0x30:
		return "m0"
	case 0x31:
		return "m1"
	case 0x32:
		return "m2"
	case 0x33:
		return "m3"
	case 0x34:
		return "m4"
	case 0x35:
		return "m5"
	default:
		return fmt.Sprintf("0x%02x", method)
	}
}

func hasContiguousVolumes(volumes []ImportedArchiveVolume) bool {
	if len(volumes) == 0 {
		return false
	}
	for i, volume := range volumes {
		if volume.VolumeIndex != i {
			return false
		}
	}
	return true
}

func isPlayableArchiveEntry(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".avi":
		return true
	default:
		return false
	}
}
