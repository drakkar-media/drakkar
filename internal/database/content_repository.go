package database

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hjongedijk/drakkar/internal/stream"
)

// computeSpans builds the SegmentSpan slice for a virtual file on-the-fly from
// the inline segment data stored in nzb_files. segmentByteOffset is the byte
// offset within the NZB file's decoded content where this virtual file starts
// (0 for direct_nzb, = archive_offset for stored_rar). entrySize is the size of
// the virtual file in bytes.
func computeSpans(messageIDs []string, decodedSegmentSize, lastDecodedSize, segmentByteOffset, entrySize int64) []stream.SegmentSpan {
	if len(messageIDs) == 0 || decodedSegmentSize <= 0 {
		return nil
	}
	entryEnd := segmentByteOffset + entrySize
	firstSegIdx := segmentByteOffset / decodedSegmentSize
	var spans []stream.SegmentSpan
	vfPos := int64(0)
	for i := firstSegIdx; i < int64(len(messageIDs)); i++ {
		segStart := i * decodedSegmentSize
		segSize := decodedSegmentSize
		if i == int64(len(messageIDs))-1 && lastDecodedSize > 0 {
			segSize = lastDecodedSize
		}
		segEnd := segStart + segSize
		if segStart >= entryEnd {
			break
		}
		dataStart := segmentByteOffset
		if segStart > dataStart {
			dataStart = segStart
		}
		dataEnd := entryEnd
		if segEnd < dataEnd {
			dataEnd = segEnd
		}
		byteInSeg := dataStart - segStart
		chunkLen := dataEnd - dataStart
		spans = append(spans, stream.SegmentSpan{
			SegmentID:        i, // segment index used as cache key in ReadAheadManager
			MessageID:        messageIDs[i],
			Start:            vfPos,
			End:              vfPos + chunkLen,
			DecodedStart:     segStart,
			SegmentByteStart: byteInSeg,
		})
		vfPos += chunkLen
	}
	return spans
}

type storedRarRangeSource struct {
	VolumePath    string
	EntryOffset   int64
	ArchiveOffset int64
	LengthBytes   int64
}

type storedRarNZBSource struct {
	MessageIDs         []string
	DecodedSegmentSize int64
	LastDecodedSize    int64
	FileSizeBytes      int64
}

type storedRarVolumeMeta struct {
	Path        string
	VolumeIndex int
}

func buildStoredRarSpans(sources map[string]storedRarNZBSource, ranges []storedRarRangeSource) []stream.SegmentSpan {
	if len(ranges) == 0 || len(sources) == 0 {
		return nil
	}
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].EntryOffset != ranges[j].EntryOffset {
			return ranges[i].EntryOffset < ranges[j].EntryOffset
		}
		if ranges[i].VolumePath != ranges[j].VolumePath {
			return ranges[i].VolumePath < ranges[j].VolumePath
		}
		return ranges[i].ArchiveOffset < ranges[j].ArchiveOffset
	})
	var (
		out           []stream.SegmentSpan
		nextSegmentID int64
		cumulative    int64
	)
	for _, item := range ranges {
		source, ok := sources[strings.ToLower(strings.TrimSpace(item.VolumePath))]
		if !ok {
			continue
		}
		// Place each volume's reconstructed spans by chaining on the actual
		// length computeSpans derives from its segment data, not the stored
		// item.EntryOffset. EntryOffset was persisted at import time from a
		// separate RAR-header-based volume-capacity calculation that can
		// drift from the true decoded byte count (e.g. it used the raw
		// encoded volume size rather than the decoded content size), which
		// otherwise leaves the placed spans with a gap or overlap at every
		// volume boundary.
		spans := computeSpans(source.MessageIDs, source.DecodedSegmentSize, source.LastDecodedSize, item.ArchiveOffset, item.LengthBytes)
		volumeStart := cumulative
		volumeLen := spanFileSize(spans)
		for _, span := range spans {
			span.Start += volumeStart
			span.End += volumeStart
			span.SegmentID = nextSegmentID
			nextSegmentID++
			out = append(out, span)
		}
		cumulative = volumeStart + volumeLen
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].End < out[j].End
	})
	return out
}

func storedRarSourceSize(source storedRarNZBSource) int64 {
	if source.FileSizeBytes > 0 && (len(source.MessageIDs) == 0 || source.DecodedSegmentSize <= 0) {
		return source.FileSizeBytes
	}
	if len(source.MessageIDs) == 0 || source.DecodedSegmentSize <= 0 {
		return 0
	}
	if len(source.MessageIDs) == 1 {
		if source.LastDecodedSize > 0 {
			return source.LastDecodedSize
		}
		return source.DecodedSegmentSize
	}
	total := int64(len(source.MessageIDs)-1) * source.DecodedSegmentSize
	if source.LastDecodedSize > 0 {
		return total + source.LastDecodedSize
	}
	return total + source.DecodedSegmentSize
}

func reconstructStoredRarRanges(
	sources map[string]storedRarNZBSource,
	volumes []storedRarVolumeMeta,
	startVolumePath string,
	startArchiveOffset int64,
	continuationOffsets map[string]int64,
	entrySize int64,
) []storedRarRangeSource {
	if len(sources) == 0 || len(volumes) == 0 || entrySize <= 0 {
		return nil
	}
	startIndex := -1
	for i, volume := range volumes {
		if strings.EqualFold(strings.TrimSpace(volume.Path), strings.TrimSpace(startVolumePath)) {
			startIndex = i
			break
		}
	}
	if startIndex < 0 {
		startIndex = 0
	}
	if startArchiveOffset < 0 {
		startArchiveOffset = 0
	}
	remaining := entrySize
	entryOffset := int64(0)
	archiveOffset := startArchiveOffset
	ranges := make([]storedRarRangeSource, 0, len(volumes)-startIndex)
	for i := startIndex; i < len(volumes) && remaining > 0; i++ {
		volume := volumes[i]
		source, ok := sources[strings.ToLower(strings.TrimSpace(volume.Path))]
		if !ok {
			return nil
		}
		if i > startIndex {
			if off, ok := continuationOffsets[strings.ToLower(strings.TrimSpace(volume.Path))]; ok && off >= 0 {
				archiveOffset = off
			}
		}
		sourceSize := storedRarSourceSize(source)
		if sourceSize <= archiveOffset {
			return nil
		}
		length := remaining
		available := sourceSize - archiveOffset
		if length > available {
			length = available
		}
		ranges = append(ranges, storedRarRangeSource{
			VolumePath:    volume.Path,
			EntryOffset:   entryOffset,
			ArchiveOffset: archiveOffset,
			LengthBytes:   length,
		})
		entryOffset += length
		remaining -= length
		archiveOffset = 0
	}
	if remaining > 0 {
		return nil
	}
	return ranges
}

func (db *DB) detectStoredRarContinuationOffsets(ctx context.Context, sources map[string]storedRarNZBSource, volumes []storedRarVolumeMeta, startVolumePath string) map[string]int64 {
	if db.SegmentFetcher == nil || len(volumes) == 0 {
		return nil
	}
	startIndex := -1
	for i, volume := range volumes {
		if strings.EqualFold(strings.TrimSpace(volume.Path), strings.TrimSpace(startVolumePath)) {
			startIndex = i
			break
		}
	}
	if startIndex < 0 {
		startIndex = 0
	}
	offsets := make(map[string]int64)
	for i := startIndex + 1; i < len(volumes); i++ {
		volume := volumes[i]
		source, ok := sources[strings.ToLower(strings.TrimSpace(volume.Path))]
		if !ok || len(source.MessageIDs) == 0 {
			continue
		}
		size := storedRarSourceSize(source)
		if size <= 0 {
			continue
		}
		limit := int64(512)
		if limit > size {
			limit = size
		}
		prefix, err := db.SegmentFetcher.FetchRange(ctx, stream.SegmentRange{
			SegmentID:    0,
			MessageID:    source.MessageIDs[0],
			RangeStart:   0,
			RangeEnd:     limit,
			SegmentStart: 0,
			SegmentEnd:   limit,
		})
		if err != nil || len(prefix) == 0 {
			continue
		}
		var dataStart int64
		if len(prefix) >= 8 && string(prefix[:8]) == "Rar!\x1a\x07\x01\x00" {
			dataStart, _ = rar5FindDataStart(prefix)
		} else {
			dataStart, _ = rar4FindDataStart(prefix)
		}
		if dataStart > 0 {
			offsets[strings.ToLower(strings.TrimSpace(volume.Path))] = dataStart
		}
	}
	return offsets
}

func (db *DB) ListContentMountEntriesForRelease(ctx context.Context, selectedReleaseID int64) ([]ContentMountEntry, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select vf.id, vf.selected_release_id, vf.path, vf.file_name, vf.size_bytes, vf.reader_kind
		from virtual_files vf
		where vf.selected_release_id = $1
		order by vf.path asc`, selectedReleaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContentMountEntry
	for rows.Next() {
		var item ContentMountEntry
		if err := rows.Scan(&item.VirtualFileID, &item.SelectedReleaseID, &item.Path, &item.FileName, &item.SizeBytes, &item.ReaderKind); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListContentMountEntries(ctx context.Context) ([]ContentMountEntry, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select
			vf.id,
			vf.selected_release_id,
			vf.path,
			vf.file_name,
			vf.size_bytes,
			vf.reader_kind
		from virtual_files vf
		order by vf.selected_release_id asc, vf.path asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContentMountEntry
	for rows.Next() {
		var item ContentMountEntry
		if err := rows.Scan(
			&item.VirtualFileID,
			&item.SelectedReleaseID,
			&item.Path,
			&item.FileName,
			&item.SizeBytes,
			&item.ReaderKind,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) OpenVirtualMediaFile(ctx context.Context, virtualFileID int64) (stream.VirtualMediaFile, error) {
	entry, err := db.loadVFCache(ctx, virtualFileID)
	if err != nil {
		return nil, err
	}
	fetcher := db.SegmentFetcher
	if fetcher == nil {
		fetcher = unavailableSegmentFetcher{}
	}
	switch entry.readerKind {
	case "inline":
		return stream.NewByteVirtualFile(entry.name, entry.inlineData), nil
	case "direct_nzb", "stored_rar":
		// Each reader gets its own copy of the spans so realignSpans adjustments
		// don't corrupt the cached canonical slice.
		spans := make([]stream.SegmentSpan, len(entry.spans))
		copy(spans, entry.spans)
		if entry.readerKind == "stored_rar" {
			return stream.NewStoredRarReader(entry.name, entry.size, spans, fetcher, db.ReadAhead), nil
		}
		return stream.NewDirectNzbReader(entry.name, entry.size, spans, fetcher, db.ReadAhead), nil
	default:
		return nil, errors.New("virtual media reader not implemented: " + entry.readerKind)
	}
}

// loadVFCache returns the cached virtual-file metadata, querying the DB on
// the first call for each virtualFileID and serving from memory thereafter.
// Spans are recomputed from inline nzb_files data, so calibration must call
// InvalidateVFCacheForNZBFile to flush stale entries after updating sizes.
func (db *DB) loadVFCache(ctx context.Context, virtualFileID int64) (*cachedVF, error) {
	db.vfCacheMu.RLock()
	if entry, ok := db.vfCache[virtualFileID]; ok {
		db.vfCacheMu.RUnlock()
		return entry, nil
	}
	db.vfCacheMu.RUnlock()

	var entry cachedVF
	var nzbFileID sql.NullInt64
	var segByteOffset, decodedSegSize, lastDecSize int64
	var messageIDsRaw *string // scan as nullable string then parse

	err := db.SQL.QueryRowContext(ctx, `
		SELECT vf.file_name, vf.reader_kind, vf.inline_bytes, vf.size_bytes,
		       vf.nzb_file_id, vf.segment_byte_offset,
		       COALESCE(nf.message_ids::text, '{}'),
		       COALESCE(nf.decoded_segment_size, 0),
		       COALESCE(nf.last_decoded_size, 0)
		FROM virtual_files vf
		LEFT JOIN nzb_files nf ON nf.id = vf.nzb_file_id
		WHERE vf.id = $1`, virtualFileID,
	).Scan(&entry.name, &entry.readerKind, &entry.inlineData, &entry.size,
		&nzbFileID, &segByteOffset, &messageIDsRaw, &decodedSegSize, &lastDecSize)
	if err != nil {
		return nil, err
	}

	if entry.readerKind == "stored_rar" {
		spans, spansErr := db.loadStoredRarSpans(ctx, virtualFileID)
		if spansErr != nil {
			return nil, spansErr
		}
		if len(spans) > 0 {
			entry.spans = spans
		}
	}
	if len(entry.spans) == 0 && (entry.readerKind == "direct_nzb" || entry.readerKind == "stored_rar") {
		var msgIDs []string
		if messageIDsRaw != nil {
			msgIDs = parsePostgresArray(*messageIDsRaw)
		}
		entry.spans = computeSpans(msgIDs, decodedSegSize, lastDecSize, segByteOffset, entry.size)
	}

	db.vfCacheMu.Lock()
	db.vfCache[virtualFileID] = &entry
	db.vfCacheMu.Unlock()
	return &entry, nil
}

func (db *DB) loadStoredRarSpans(ctx context.Context, virtualFileID int64) ([]stream.SegmentSpan, error) {
	var (
		selectedReleaseID  int64
		virtualFileSize    int64
		startArchiveOffset int64
		startVolumePath    string
	)
	if err := db.SQL.QueryRowContext(ctx, `
		SELECT vf.selected_release_id,
		       vf.size_bytes,
		       vf.segment_byte_offset,
		       COALESCE(nf.subject, '')
		FROM virtual_files vf
		LEFT JOIN nzb_files nf ON nf.id = vf.nzb_file_id
		WHERE vf.id = $1`, virtualFileID,
	).Scan(&selectedReleaseID, &virtualFileSize, &startArchiveOffset, &startVolumePath); err != nil {
		return nil, err
	}
	startVolumePath = filepath.Base(strings.TrimSpace(parseNZBSubjectFilename(startVolumePath)))

	rows, err := db.SQL.QueryContext(ctx, `
		SELECT av.path, ar.entry_offset, ar.archive_offset, ar.length_bytes
		FROM virtual_files vf
		JOIN archives a ON a.selected_release_id = vf.selected_release_id
		JOIN archive_entries ae ON ae.archive_id = a.id AND ae.path = vf.file_name
		JOIN archive_ranges ar ON ar.archive_entry_id = ae.id
		JOIN archive_volumes av ON av.id = ar.archive_volume_id
		WHERE vf.id = $1
		ORDER BY ar.entry_offset ASC, ar.archive_offset ASC`, virtualFileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ranges []storedRarRangeSource
	for rows.Next() {
		var item storedRarRangeSource
		if err := rows.Scan(&item.VolumePath, &item.EntryOffset, &item.ArchiveOffset, &item.LengthBytes); err != nil {
			return nil, err
		}
		ranges = append(ranges, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sourceRows, err := db.SQL.QueryContext(ctx, `
		SELECT nf.subject, COALESCE(nf.message_ids::text, '{}'),
		       COALESCE(nf.decoded_segment_size, 0),
		       COALESCE(nf.last_decoded_size, 0),
		       COALESCE(nf.file_size_bytes, 0)
		FROM nzb_files nf
		JOIN nzb_documents nd ON nd.id = nf.nzb_document_id
		WHERE nd.selected_release_id = $1`, selectedReleaseID)
	if err != nil {
		return nil, err
	}
	defer sourceRows.Close()

	sources := make(map[string]storedRarNZBSource)
	for sourceRows.Next() {
		var (
			subject       string
			messageIDsRaw string
			source        storedRarNZBSource
		)
		if err := sourceRows.Scan(&subject, &messageIDsRaw, &source.DecodedSegmentSize, &source.LastDecodedSize, &source.FileSizeBytes); err != nil {
			return nil, err
		}
		source.MessageIDs = parsePostgresArray(messageIDsRaw)
		name := strings.ToLower(filepath.Base(strings.TrimSpace(parseNZBSubjectFilename(subject))))
		if name == "" {
			name = strings.ToLower(filepath.Base(strings.TrimSpace(subject)))
		}
		if name != "" {
			sources[name] = source
		}
	}
	if err := sourceRows.Err(); err != nil {
		return nil, err
	}
	if len(ranges) > 0 {
		startVolumePath = ranges[0].VolumePath
		startArchiveOffset = ranges[0].ArchiveOffset
	}

	spans := buildStoredRarSpans(sources, ranges)
	size := spanFileSize(spans)
	if size == virtualFileSize {
		return spans, nil
	}
	if size > virtualFileSize {
		// Overshoot: archive_ranges mapped more bytes than the entry's actual
		// unpacked size (e.g. a header-parsed packed size that included a few
		// bytes of RAR container overhead per volume). The leading bytes are
		// still correctly positioned, so trim the tail back to the true
		// boundary instead of discarding an otherwise-valid reconstruction.
		return truncateSpans(spans, virtualFileSize), nil
	}
	// Undershoot: archive_ranges only covers a prefix of the volumes (e.g. a
	// header-parsed packed size far smaller than the real archive caused
	// import-time range assignment to stop after the first volume or two,
	// never reaching the rest). Reconstruct the missing volumes' ranges from
	// the full volume list using the standard RAR continuation convention
	// (each continuation volume's file data starts at its own offset 0)
	// rather than falling back to a single-nzb_file computation that can
	// never cover a multi-volume archive's true size.
	volumes, err := db.loadStoredRarVolumes(ctx, selectedReleaseID)
	if err != nil {
		return nil, err
	}
	if reconstructed := reconstructStoredRarRanges(sources, volumes, startVolumePath, startArchiveOffset, nil, virtualFileSize); reconstructed != nil {
		if rebuilt := buildStoredRarSpans(sources, reconstructed); spanFileSize(rebuilt) == virtualFileSize {
			return rebuilt, nil
		}
	}
	// Still no match — return nil so the caller falls back to message-ID-based
	// span computation (correct only for single-volume direct_nzb-shaped
	// entries, but no worse than what could be reconstructed here).
	slog.Debug("loadStoredRarSpans: could not reconstruct a matching layout",
		"virtualFileID", virtualFileID, "spanSize", size, "expectedSize", virtualFileSize,
		"numRanges", len(ranges), "numSources", len(sources), "numVolumes", len(volumes))
	return nil, nil
}

// loadStoredRarVolumes returns every RAR volume for a release, ordered by
// volume_index, regardless of whether archive_ranges has a row for it yet.
func (db *DB) loadStoredRarVolumes(ctx context.Context, selectedReleaseID int64) ([]storedRarVolumeMeta, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT av.path, av.volume_index
		FROM archive_volumes av
		JOIN archives a ON a.id = av.archive_id
		WHERE a.selected_release_id = $1
		ORDER BY av.volume_index ASC`, selectedReleaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var volumes []storedRarVolumeMeta
	for rows.Next() {
		var v storedRarVolumeMeta
		if err := rows.Scan(&v.Path, &v.VolumeIndex); err != nil {
			return nil, err
		}
		volumes = append(volumes, v)
	}
	return volumes, rows.Err()
}

// InvalidateVFCacheForNZBFile clears all cached virtual-file entries so that
// the next open picks up corrected decoded_segment_size / last_decoded_size
// values written by calibration. Simplest correct approach: clear everything.
func (db *DB) InvalidateVFCacheForNZBFile(_ int64) {
	db.vfCacheMu.Lock()
	db.vfCache = make(map[int64]*cachedVF)
	db.vfCacheMu.Unlock()
}

type unavailableSegmentFetcher struct{}

func (unavailableSegmentFetcher) FetchRange(ctx context.Context, segment stream.SegmentRange) ([]byte, error) {
	return nil, errors.New("direct_nzb fetcher unavailable: nntp not implemented yet")
}

// ListAllVirtualFiles returns a lightweight list of all published virtual files
// for the WebDAV directory listing (used by rclone to mount the content).
func (db *DB) ListAllVirtualFiles(ctx context.Context) ([]VirtualFileEntry, error) {
	rows, err := db.SQL.QueryContext(ctx, `SELECT id, file_name, size_bytes FROM virtual_files ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VirtualFileEntry
	for rows.Next() {
		var e VirtualFileEntry
		if err := rows.Scan(&e.ID, &e.FileName, &e.Size); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type VirtualFileEntry struct {
	ID       int64
	FileName string
	Size     int64
}

func spanFileSize(spans []stream.SegmentSpan) int64 {
	var end int64
	for _, span := range spans {
		if span.End > end {
			end = span.End
		}
	}
	return end
}

// truncateSpans drops or clips spans so the total coverage is exactly size.
// spans must be sorted by Start (buildStoredRarSpans already sorts them).
func truncateSpans(spans []stream.SegmentSpan, size int64) []stream.SegmentSpan {
	out := make([]stream.SegmentSpan, 0, len(spans))
	for _, span := range spans {
		if span.Start >= size {
			break
		}
		if span.End > size {
			span.End = size
		}
		out = append(out, span)
	}
	return out
}
