package database

import (
	"context"
	"database/sql"
	"errors"

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

	if entry.readerKind == "direct_nzb" || entry.readerKind == "stored_rar" {
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
