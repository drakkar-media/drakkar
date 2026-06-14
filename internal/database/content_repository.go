package database

import (
	"context"
	"errors"

	"github.com/hjongedijk/drakkar/internal/stream"
)

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
			return stream.NewStoredRarReader(entry.name, spanFileSize(spans), spans, fetcher, db.ReadAhead), nil
		}
		return stream.NewDirectNzbReader(entry.name, spanFileSize(spans), spans, fetcher, db.ReadAhead), nil
	default:
		return nil, errors.New("virtual media reader not implemented: " + entry.readerKind)
	}
}

// loadVFCache returns the cached virtual-file metadata, querying the DB on
// the first call for each virtualFileID and serving from memory thereafter.
// Spans are immutable in the DB after the NZB is imported, so the cache
// never needs invalidation.
func (db *DB) loadVFCache(ctx context.Context, virtualFileID int64) (*cachedVF, error) {
	db.vfCacheMu.RLock()
	if entry, ok := db.vfCache[virtualFileID]; ok {
		db.vfCacheMu.RUnlock()
		return entry, nil
	}
	db.vfCacheMu.RUnlock()

	var entry cachedVF
	err := db.SQL.QueryRowContext(ctx, `
		select file_name, reader_kind, inline_bytes
		from virtual_files
		where id = $1`, virtualFileID,
	).Scan(&entry.name, &entry.readerKind, &entry.inlineData)
	if err != nil {
		return nil, err
	}
	if entry.readerKind == "direct_nzb" || entry.readerKind == "stored_rar" {
		rows, err := db.SQL.QueryContext(ctx, `
			select ns.id, ns.message_id, vfr.range_start, vfr.range_end
			from virtual_file_ranges vfr
			join nzb_segments ns on ns.id = vfr.nzb_segment_id
			where vfr.virtual_file_id = $1
			order by vfr.range_start asc`, virtualFileID,
		)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var span stream.SegmentSpan
			if err := rows.Scan(&span.SegmentID, &span.MessageID, &span.Start, &span.End); err != nil {
				return nil, err
			}
			entry.spans = append(entry.spans, span)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	db.vfCacheMu.Lock()
	db.vfCache[virtualFileID] = &entry
	db.vfCacheMu.Unlock()
	return &entry, nil
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
