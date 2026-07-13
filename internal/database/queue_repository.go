package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/drakkar-media/drakkar/internal/policy"
)

type importedFileSegments struct {
	fileName  string
	nzbFileID int64
}

// insertImportedFiles inserts one nzb_files row per imported file, plus a
// virtual_files row for any that look like real playable media (skipping
// samples/stubs). Shared by CreateImportedNZB and ImportSelectedReleaseNZB,
// which previously carried a byte-for-byte identical copy of this loop —
// any future fix (e.g. to playable-media detection) needed to land twice.
func insertImportedFiles(ctx context.Context, tx *sql.Tx, selectedReleaseID, nzbDocumentID int64, files []ImportedNZBFile) (map[string]importedFileSegments, error) {
	fileSegments := make(map[string]importedFileSegments, len(files))
	for _, file := range files {
		var postedAt any
		if file.PostedUnix > 0 {
			postedAt = time.Unix(file.PostedUnix, 0).UTC()
		}
		msgIDs := make([]string, len(file.Segments))
		for i, s := range file.Segments {
			msgIDs[i] = s.MessageID
		}
		decSegSize, lastDecSize := segmentSizes(file.Segments)
		var nzbFileID int64
		if err := tx.QueryRowContext(ctx, `
			insert into nzb_files (nzb_document_id, subject, poster, posted_at, file_size_bytes, message_ids, decoded_segment_size, last_decoded_size)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			returning id`,
			nzbDocumentID, file.Subject, file.Poster, postedAt, file.FileSizeBytes,
			pgTextArray(msgIDs), decSegSize, lastDecSize,
		).Scan(&nzbFileID); err != nil {
			return nil, err
		}

		fileSegments[file.FileName] = importedFileSegments{
			fileName:  file.FileName,
			nzbFileID: nzbFileID,
		}

		if isPlayableMedia(file.FileName, file.FileSizeBytes) {
			virtualPath := "releases/" + fmt.Sprintf("%d", selectedReleaseID) + "/" + file.FileName
			if err := tx.QueryRowContext(ctx, `
				insert into virtual_files (
					selected_release_id, path, file_name, size_bytes, reader_kind,
					nzb_file_id, segment_byte_offset
				) values ($1, $2, $3, $4, 'direct_nzb', $5, 0)
				returning id`,
				selectedReleaseID, virtualPath, file.FileName, file.FileSizeBytes, nzbFileID,
			).Scan(new(int64)); err != nil {
				return nil, err
			}
		}
	}
	return fileSegments, nil
}

func (db *DB) ListQueue(ctx context.Context) ([]QueueSnapshot, error) {
	// Return active items (all states except requested) + last 200 available/failed.
	// Skipping 'requested' items keeps the response fast — there can be thousands
	// and they're not actionable until the search worker picks them up.
	rows, err := db.SQL.QueryContext(ctx, `
		with active as (
			select q.id from queue_items q
			where q.state not in ('requested', 'available', 'failed')
		),
		recent_history as (
			select q.id from queue_items q
			where q.state in ('available', 'failed')
			order by q.updated_at desc, q.id desc
			limit 200
		),
		ids as (select id from active union select id from recent_history)
		select
			q.id,
			q.library_item_id,
			l.title,
			q.state,
			q.failure_reason,
			q.idempotency_key,
			q.selected_release_id,
			n.id,
			coalesce(n.file_name, ''),
			coalesce((select count(*) from nzb_files nf where nf.nzb_document_id = n.id), 0),
			coalesce((select sum(array_length(nf.message_ids, 1)) from nzb_files nf where nf.nzb_document_id = n.id), 0),
			q.created_at,
			q.updated_at
		from queue_items q
		join ids on ids.id = q.id
		join library_items l on l.id = q.library_item_id
		left join selected_releases sr on sr.id = q.selected_release_id
		left join lateral (
			select id, file_name
			from nzb_documents
			where selected_release_id = sr.id
			order by id desc limit 1
		) n on sr.id is not null
		order by
			case q.state
				when 'fetching_nzb' then 0
				when 'indexing' then 1
				when 'preflight' then 2
				when 'publishing' then 3
				when 'selected' then 4
				when 'ranking' then 5
				when 'searching' then 6
				when 'available' then 7
				when 'failed' then 8
				else 9
			end asc,
			q.updated_at desc,
			q.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []QueueSnapshot
	for rows.Next() {
		var item QueueSnapshot
		var selectedRelease sql.NullInt64
		var nzbDocument sql.NullInt64
		if err := rows.Scan(
			&item.QueueItemID,
			&item.LibraryItemID,
			&item.LibraryTitle,
			&item.State,
			&item.FailureReason,
			&item.IdempotencyKey,
			&selectedRelease,
			&nzbDocument,
			&item.NZBFileName,
			&item.NZBFileCount,
			&item.NZBSegmentCount,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if selectedRelease.Valid {
			value := selectedRelease.Int64
			item.SelectedRelease = &value
		}
		if nzbDocument.Valid {
			value := nzbDocument.Int64
			item.NZBDocumentID = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) CreateImportedNZB(ctx context.Context, imported ImportedNZB) (QueueSnapshot, error) {
	imported = db.applyImportPolicies(ctx, imported)
	imported.Archives = inspectImportedArchives(ctx, imported.Archives, imported.Files, db.SegmentFetcher)
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return QueueSnapshot{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var snapshot QueueSnapshot
	var existing bool
	err = tx.QueryRowContext(ctx, `select exists(select 1 from queue_items where idempotency_key = $1)`, imported.IdempotencyKey).Scan(&existing)
	if err != nil {
		return QueueSnapshot{}, err
	}
	if existing {
		if err := tx.Rollback(); err != nil {
			return QueueSnapshot{}, err
		}
		items, err := db.ListQueue(ctx)
		if err != nil {
			return QueueSnapshot{}, err
		}
		for _, item := range items {
			if item.IdempotencyKey == imported.IdempotencyKey {
				return item, nil
			}
		}
		return QueueSnapshot{}, errors.New("existing queue item not found after idempotency hit")
	}

	mediaType := imported.MediaType
	if mediaType == "" {
		mediaType = "manual_nzb"
	}
	var libraryItemID int64
	if err = tx.QueryRowContext(ctx, `
		insert into library_items (media_type, title)
		values ($1, $2)
		returning id`, mediaType, imported.FileName).Scan(&libraryItemID); err != nil {
		return QueueSnapshot{}, err
	}

	var releaseCandidateID int64
	if err = tx.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, score, custom_format_score, selected)
		values ($1, $2, 0, 0, true)
		returning id`, libraryItemID, imported.FileName).Scan(&releaseCandidateID); err != nil {
		return QueueSnapshot{}, err
	}

	var selectedReleaseID int64
	if err = tx.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libraryItemID, releaseCandidateID).Scan(&selectedReleaseID); err != nil {
		return QueueSnapshot{}, err
	}

	var nzbDocumentID int64
	if err = tx.QueryRowContext(ctx, `
		insert into nzb_documents (selected_release_id, file_name, xml)
		values ($1, $2, $3)
		returning id`, selectedReleaseID, imported.FileName, compressNZBXML(imported.XML)).Scan(&nzbDocumentID); err != nil {
		return QueueSnapshot{}, err
	}

	fileSegments, err := insertImportedFiles(ctx, tx, selectedReleaseID, nzbDocumentID, imported.Files)
	if err != nil {
		return QueueSnapshot{}, err
	}
	if err = insertImportedArchives(ctx, tx, selectedReleaseID, imported.Archives, fileSegments); err != nil {
		return QueueSnapshot{}, err
	}

	if err = tx.QueryRowContext(ctx, `
		insert into queue_items (library_item_id, state, idempotency_key, selected_release_id)
		values ($1, $2, $3, $4)
		returning id, created_at, updated_at`,
		libraryItemID, QueueIndexing, imported.IdempotencyKey, selectedReleaseID,
	).Scan(&snapshot.QueueItemID, &snapshot.CreatedAt, &snapshot.UpdatedAt); err != nil {
		return QueueSnapshot{}, err
	}

	snapshot.LibraryItemID = libraryItemID
	snapshot.LibraryTitle = imported.FileName
	snapshot.State = QueueIndexing
	snapshot.IdempotencyKey = imported.IdempotencyKey
	snapshot.SelectedRelease = &selectedReleaseID
	snapshot.NZBDocumentID = &nzbDocumentID
	snapshot.NZBFileName = imported.FileName
	snapshot.NZBFileCount = imported.FileCount
	snapshot.NZBSegmentCount = imported.SegmentCount

	if err = tx.Commit(); err != nil {
		return QueueSnapshot{}, err
	}
	return snapshot, nil
}

// AttachImportedNZBToLibraryItem attaches a manually-uploaded NZB directly to
// an existing library item (movie or a specific TV episode), bypassing the
// indexer search/candidate pipeline entirely. Mirrors CreateImportedNZB but
// reuses the given library item instead of creating a new one, and upserts
// its queue_items row instead of inserting a fresh one.
func (db *DB) AttachImportedNZBToLibraryItem(ctx context.Context, libraryItemID int64, imported ImportedNZB) (QueueSnapshot, error) {
	imported = db.applyImportPolicies(ctx, imported)
	imported.Archives = inspectImportedArchives(ctx, imported.Archives, imported.Files, db.SegmentFetcher)
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return QueueSnapshot{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var libraryTitle string
	if err = tx.QueryRowContext(ctx, `select title from library_items where id = $1`, libraryItemID).Scan(&libraryTitle); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QueueSnapshot{}, fmt.Errorf("library item %d not found", libraryItemID)
		}
		return QueueSnapshot{}, err
	}

	var existing bool
	if err = tx.QueryRowContext(ctx, `select exists(select 1 from queue_items where idempotency_key = $1)`, imported.IdempotencyKey).Scan(&existing); err != nil {
		return QueueSnapshot{}, err
	}
	if existing {
		if err := tx.Rollback(); err != nil {
			return QueueSnapshot{}, err
		}
		items, err := db.ListQueue(ctx)
		if err != nil {
			return QueueSnapshot{}, err
		}
		for _, item := range items {
			if item.IdempotencyKey == imported.IdempotencyKey {
				return item, nil
			}
		}
		return QueueSnapshot{}, errors.New("existing queue item not found after idempotency hit")
	}

	if err = preDeleteVFRByLibraryItem(ctx, tx, libraryItemID); err != nil {
		return QueueSnapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, `delete from selected_releases where library_item_id = $1`, libraryItemID); err != nil {
		return QueueSnapshot{}, err
	}

	var releaseCandidateID int64
	if err = tx.QueryRowContext(ctx, `
		insert into release_candidates (library_item_id, title, score, custom_format_score, selected)
		values ($1, $2, 0, 0, true)
		returning id`, libraryItemID, imported.FileName).Scan(&releaseCandidateID); err != nil {
		return QueueSnapshot{}, err
	}

	var selectedReleaseID int64
	if err = tx.QueryRowContext(ctx, `
		insert into selected_releases (library_item_id, release_candidate_id)
		values ($1, $2)
		returning id`, libraryItemID, releaseCandidateID).Scan(&selectedReleaseID); err != nil {
		return QueueSnapshot{}, err
	}

	var nzbDocumentID int64
	if err = tx.QueryRowContext(ctx, `
		insert into nzb_documents (selected_release_id, file_name, xml)
		values ($1, $2, $3)
		returning id`, selectedReleaseID, imported.FileName, compressNZBXML(imported.XML)).Scan(&nzbDocumentID); err != nil {
		return QueueSnapshot{}, err
	}

	fileSegments, err := insertImportedFiles(ctx, tx, selectedReleaseID, nzbDocumentID, imported.Files)
	if err != nil {
		return QueueSnapshot{}, err
	}
	if err = insertImportedArchives(ctx, tx, selectedReleaseID, imported.Archives, fileSegments); err != nil {
		return QueueSnapshot{}, err
	}

	var snapshot QueueSnapshot
	tag, updateErr := tx.ExecContext(ctx, `
		update queue_items
		set state = $2, failure_reason = '', idempotency_key = $3, selected_release_id = $4, updated_at = now()
		where library_item_id = $1`,
		libraryItemID, QueueIndexing, imported.IdempotencyKey, selectedReleaseID,
	)
	if updateErr != nil {
		err = updateErr
		return QueueSnapshot{}, err
	}
	rows, _ := tag.RowsAffected()
	if rows == 0 {
		if err = tx.QueryRowContext(ctx, `
			insert into queue_items (library_item_id, state, idempotency_key, selected_release_id)
			values ($1, $2, $3, $4)
			returning id`,
			libraryItemID, QueueIndexing, imported.IdempotencyKey, selectedReleaseID,
		).Scan(&snapshot.QueueItemID); err != nil {
			return QueueSnapshot{}, err
		}
	}
	if err = tx.QueryRowContext(ctx, `
		select id, created_at, updated_at from queue_items where library_item_id = $1`, libraryItemID,
	).Scan(&snapshot.QueueItemID, &snapshot.CreatedAt, &snapshot.UpdatedAt); err != nil {
		return QueueSnapshot{}, err
	}

	snapshot.LibraryItemID = libraryItemID
	snapshot.LibraryTitle = libraryTitle
	snapshot.State = QueueIndexing
	snapshot.IdempotencyKey = imported.IdempotencyKey
	snapshot.SelectedRelease = &selectedReleaseID
	snapshot.NZBDocumentID = &nzbDocumentID
	snapshot.NZBFileName = imported.FileName
	snapshot.NZBFileCount = imported.FileCount
	snapshot.NZBSegmentCount = imported.SegmentCount

	if err = tx.Commit(); err != nil {
		return QueueSnapshot{}, err
	}
	return snapshot, nil
}

func (db *DB) ImportSelectedReleaseNZB(ctx context.Context, selectedReleaseID int64, imported ImportedNZB) (QueueSnapshot, error) {
	imported = db.applyImportPolicies(ctx, imported)
	imported.Archives = inspectImportedArchives(ctx, imported.Archives, imported.Files, db.SegmentFetcher)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return QueueSnapshot{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var snapshot QueueSnapshot
	err = tx.QueryRowContext(ctx, `
		select q.id, q.library_item_id, l.title, q.idempotency_key, q.created_at
		from queue_items q
		join library_items l on l.id = q.library_item_id
		where q.selected_release_id = $1`, selectedReleaseID,
	).Scan(&snapshot.QueueItemID, &snapshot.LibraryItemID, &snapshot.LibraryTitle, &snapshot.IdempotencyKey, &snapshot.CreatedAt)
	if err != nil {
		return QueueSnapshot{}, err
	}

	// virtual_files.nzb_file_id → nzb_files(id) has no ON DELETE CASCADE, so
	// virtual_files must be deleted before nzb_documents (which cascades to nzb_files).
	if _, err = tx.ExecContext(ctx, `
		delete from virtual_files
		where selected_release_id = $1`, selectedReleaseID); err != nil {
		return QueueSnapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		delete from archives
		where selected_release_id = $1`, selectedReleaseID); err != nil {
		return QueueSnapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		delete from nzb_documents
		where selected_release_id = $1`, selectedReleaseID); err != nil {
		return QueueSnapshot{}, err
	}

	var nzbDocumentID int64
	if err = tx.QueryRowContext(ctx, `
		insert into nzb_documents (selected_release_id, external_url, file_name, xml)
		values ($1, $2, $3, $4)
		returning id`, selectedReleaseID, imported.ExternalURL, imported.FileName, compressNZBXML(imported.XML)).Scan(&nzbDocumentID); err != nil {
		return QueueSnapshot{}, err
	}

	fileSegments, err := insertImportedFiles(ctx, tx, selectedReleaseID, nzbDocumentID, imported.Files)
	if err != nil {
		return QueueSnapshot{}, err
	}
	if err = insertImportedArchives(ctx, tx, selectedReleaseID, imported.Archives, fileSegments); err != nil {
		return QueueSnapshot{}, err
	}

	if err = tx.QueryRowContext(ctx, `
		update queue_items
		set state = $2, failure_reason = '', updated_at = now()
		where id = $1
		returning updated_at`, snapshot.QueueItemID, QueueIndexing,
	).Scan(&snapshot.UpdatedAt); err != nil {
		return QueueSnapshot{}, err
	}

	snapshot.State = QueueIndexing
	snapshot.SelectedRelease = &selectedReleaseID
	snapshot.NZBDocumentID = &nzbDocumentID
	snapshot.NZBFileName = imported.FileName
	snapshot.NZBFileCount = imported.FileCount
	snapshot.NZBSegmentCount = imported.SegmentCount

	if err = tx.Commit(); err != nil {
		return QueueSnapshot{}, err
	}
	return snapshot, nil
}

// insertImportedArchives inserts one archives row per archive, then
// batch-inserts its volumes, entries, and ranges — one round trip per level
// per archive instead of one round trip per volume/entry/range. A season-pack
// RAR set with dozens of episodes and volumes previously took hundreds of
// sequential INSERT...RETURNING round trips here.
func insertImportedArchives(ctx context.Context, tx *sql.Tx, selectedReleaseID int64, archives []ImportedArchive, fileSegments map[string]importedFileSegments) error {
	for _, archive := range archives {
		var archiveID int64
		if err := tx.QueryRowContext(ctx, `
			insert into archives (selected_release_id, kind, status, reject_reason)
			values ($1, $2, $3, $4)
			returning id`,
			selectedReleaseID,
			archive.Kind,
			archive.Status,
			archive.RejectReason,
		).Scan(&archiveID); err != nil {
			return err
		}

		volumeIDs, volumePaths, err := insertArchiveVolumes(ctx, tx, archiveID, archive.Volumes)
		if err != nil {
			return err
		}
		if len(archive.Entries) == 0 {
			continue
		}

		entryIDByPath, err := insertArchiveEntries(ctx, tx, archiveID, archive.Entries)
		if err != nil {
			return err
		}
		if err := insertArchiveRanges(ctx, tx, archive.Entries, entryIDByPath, volumeIDs); err != nil {
			return err
		}

		for _, entry := range archive.Entries {
			if archive.Status != "supported" || !isPlayableMedia(entry.Path, entry.SizeBytes) || len(entry.Ranges) == 0 {
				continue
			}
			if _, err := insertArchiveVirtualFile(ctx, tx, selectedReleaseID, entry, volumePaths, fileSegments); err != nil {
				return err
			}
		}
	}
	return nil
}

// insertArchiveVolumes batch-inserts every volume of one archive in a single
// round trip. Returns the new row IDs keyed by volume index (not by scan
// order, which unnest-backed multi-row inserts don't strictly guarantee to
// match input order).
func insertArchiveVolumes(ctx context.Context, tx *sql.Tx, archiveID int64, volumes []ImportedArchiveVolume) (map[int]int64, map[int]string, error) {
	volumeIDs := make(map[int]int64, len(volumes))
	volumePaths := make(map[int]string, len(volumes))
	if len(volumes) == 0 {
		return volumeIDs, volumePaths, nil
	}
	paths := make([]string, len(volumes))
	indexes := make([]int32, len(volumes))
	for i, v := range volumes {
		paths[i] = v.Path
		indexes[i] = int32(v.VolumeIndex)
		volumePaths[v.VolumeIndex] = v.Path
	}
	rows, err := tx.QueryContext(ctx, `
		insert into archive_volumes (archive_id, path, volume_index)
		select $1, p, i from unnest($2::text[], $3::int[]) as t(p, i)
		returning id, volume_index`,
		archiveID, paths, indexes)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var idx int
		if err := rows.Scan(&id, &idx); err != nil {
			return nil, nil, err
		}
		volumeIDs[idx] = id
	}
	return volumeIDs, volumePaths, rows.Err()
}

// insertArchiveEntries batch-inserts every entry of one archive in a single
// round trip. Returns the new row IDs keyed by path — unique per archive, so
// safe to key by rather than relying on RETURNING row order.
func insertArchiveEntries(ctx context.Context, tx *sql.Tx, archiveID int64, entries []ImportedArchiveEntry) (map[string]int64, error) {
	paths := make([]string, len(entries))
	sizes := make([]int64, len(entries))
	packedSizes := make([]int64, len(entries))
	compressions := make([]string, len(entries))
	encrypteds := make([]bool, len(entries))
	solids := make([]bool, len(entries))
	sourceVolumeIndexes := make([]int32, len(entries))
	sourceOffsets := make([]int64, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
		sizes[i] = e.SizeBytes
		packedSizes[i] = e.PackedSizeBytes
		compressions[i] = e.CompressionMethod
		encrypteds[i] = e.Encrypted
		solids[i] = e.Solid
		sourceVolumeIndexes[i] = int32(e.VolumeIndex)
		sourceOffsets[i] = e.ArchiveOffset
	}
	rows, err := tx.QueryContext(ctx, `
		insert into archive_entries (
			archive_id, path, size_bytes, packed_size_bytes,
			compression_method, encrypted, solid, source_volume_index, source_archive_offset
		)
		select $1, p, sz, psz, cm, enc, sol, svi, sao
		from unnest($2::text[], $3::bigint[], $4::bigint[], $5::text[], $6::bool[], $7::bool[], $8::int[], $9::bigint[])
			as t(p, sz, psz, cm, enc, sol, svi, sao)
		returning id, path`,
		archiveID, paths, sizes, packedSizes, compressions, encrypteds, solids, sourceVolumeIndexes, sourceOffsets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entryIDByPath := make(map[string]int64, len(entries))
	for rows.Next() {
		var id int64
		var path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		entryIDByPath[path] = id
	}
	return entryIDByPath, rows.Err()
}

// insertArchiveRanges batch-inserts every byte range across every entry of
// one archive in a single round trip.
func insertArchiveRanges(ctx context.Context, tx *sql.Tx, entries []ImportedArchiveEntry, entryIDByPath map[string]int64, volumeIDs map[int]int64) error {
	var entryIDs, archiveVolumeIDs, entryOffsets, archiveOffsets, lengths []int64
	for _, entry := range entries {
		entryID, ok := entryIDByPath[entry.Path]
		if !ok {
			continue
		}
		for _, item := range entry.Ranges {
			archiveVolumeID, ok := volumeIDs[item.VolumeIndex]
			if !ok {
				continue
			}
			entryIDs = append(entryIDs, entryID)
			archiveVolumeIDs = append(archiveVolumeIDs, archiveVolumeID)
			entryOffsets = append(entryOffsets, item.EntryOffset)
			archiveOffsets = append(archiveOffsets, item.ArchiveOffset)
			lengths = append(lengths, item.LengthBytes)
		}
	}
	if len(entryIDs) == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		insert into archive_ranges (archive_entry_id, archive_volume_id, entry_offset, archive_offset, length_bytes)
		select e, v, eo, ao, l
		from unnest($1::bigint[], $2::bigint[], $3::bigint[], $4::bigint[], $5::bigint[])
			as t(e, v, eo, ao, l)`,
		entryIDs, archiveVolumeIDs, entryOffsets, archiveOffsets, lengths)
	return err
}

func insertArchiveVirtualFile(ctx context.Context, tx *sql.Tx, selectedReleaseID int64, entry ImportedArchiveEntry, volumePaths map[int]string, fileSegments map[string]importedFileSegments) (int64, error) {
	// For stored_rar entries that span multiple volumes, only the first range's
	// NZB file and archive offset are stored — multi-volume RAR is not supported
	// for streaming, so we use the first volume's source as the reference.
	var nzbFileID int64
	archiveByteOffset := entry.ArchiveOffset // byte offset in the NZB file decoded content
	for _, item := range entry.Ranges {
		volumePath, ok := volumePaths[item.VolumeIndex]
		if !ok {
			continue
		}
		source, ok := fileSegments[volumePath]
		if !ok {
			continue
		}
		nzbFileID = source.nzbFileID
		archiveByteOffset = item.ArchiveOffset
		break
	}

	virtualPath := "releases/" + fmt.Sprintf("%d", selectedReleaseID) + "/" + entry.Path
	var virtualFileID int64
	if err := tx.QueryRowContext(ctx, `
		insert into virtual_files (
			selected_release_id, path, file_name, size_bytes, reader_kind,
			nzb_file_id, segment_byte_offset
		) values ($1, $2, $3, $4, 'stored_rar', $5, $6)
		returning id`,
		selectedReleaseID, virtualPath, entry.Path, entry.SizeBytes,
		nzbFileID, archiveByteOffset,
	).Scan(&virtualFileID); err != nil {
		return 0, err
	}
	return virtualFileID, nil
}

// segmentSizes returns (decodedSegmentSize, lastDecodedSize) from the imported segments.
// decodedSegmentSize is the size of the first (uniform) segment; lastDecodedSize is the
// size of the final segment (which may differ).
func segmentSizes(segments []ImportedNZBSegment) (int64, int64) {
	if len(segments) == 0 {
		return 0, 0
	}
	first := segments[0].DecodedEndOffset - segments[0].DecodedStartOffset
	last := segments[len(segments)-1].DecodedEndOffset - segments[len(segments)-1].DecodedStartOffset
	return first, last
}

func (db *DB) MarkSelectedReleaseFetching(ctx context.Context, selectedReleaseID int64) error {
	_, err := db.SQL.ExecContext(ctx, `
		update queue_items
		set state = $2, failure_reason = '', updated_at = now()
		where selected_release_id = $1`, selectedReleaseID, QueueFetchingNZB)
	return err
}

// StoreRawNZBDocument persists the raw NZB bytes immediately after download,
// before any preflight or segment indexing. This ensures NZBDocumentID is set
// even if the app crashes during subsequent processing — preventing a re-download
// from the indexer on the next attempt (the NZBDocumentID != nil check skips the fetch).
// ImportSelectedReleaseNZB will later overwrite this row with the full indexed version.
func (db *DB) StoreRawNZBDocument(ctx context.Context, selectedReleaseID int64, fileName string, xml []byte, externalURL string) error {
	_, err := db.SQL.ExecContext(ctx, `
		insert into nzb_documents (selected_release_id, external_url, file_name, xml)
		select $1, $2, $3, $4
		where not exists (
			select 1 from nzb_documents where selected_release_id = $1
		)`,
		selectedReleaseID, externalURL, fileName, compressNZBXML(xml))
	return err
}

func (db *DB) SetImportedNZBIndexed(ctx context.Context, queueItemID int64) error {
	_, err := db.SQL.ExecContext(ctx, `
		update queue_items
		set state = $2, updated_at = now()
		where id = $1`, queueItemID, QueuePreflight)
	return err
}

func (db *DB) MarkQueueItemPublishing(ctx context.Context, queueItemID int64) error {
	_, err := db.SQL.ExecContext(ctx, `
		update queue_items
		set state = $2, updated_at = now()
		where id = $1`, queueItemID, QueuePublishing)
	return err
}

func (db *DB) CancelNZBDocument(ctx context.Context, nzbDocumentID int64) error {
	result, err := db.SQL.ExecContext(ctx, `
		update queue_items
		set state = $2, failure_reason = 'cancelled', updated_at = now()
		where selected_release_id in (
			select selected_release_id from nzb_documents where id = $1
		)`, nzbDocumentID, QueueFailed)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("nzb document %d not found", nzbDocumentID)
	}
	return nil
}

func (db *DB) ListNZBMountEntries(ctx context.Context) ([]NZBMountEntry, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select
			n.id,
			n.file_name,
			n.xml,
			q.state
		from nzb_documents n
		join selected_releases sr on sr.id = n.selected_release_id
		join queue_items q on q.selected_release_id = sr.id
		where q.state not in ($1, $2)
		order by n.id asc`, QueueFailed, QueueAvailable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NZBMountEntry
	for rows.Next() {
		var item NZBMountEntry
		if err := rows.Scan(&item.DocumentID, &item.FileName, &item.XML, &item.State); err != nil {
			return nil, err
		}
		var decErr error
		if item.XML, decErr = decompressNZBXML(item.XML); decErr != nil {
			return nil, decErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// minPlayableFileSizeBytes is the minimum size for a virtual media file.
// Files smaller than this are stubs, extras, or corrupt placeholders — they
// would show as "Video: none / Audio: none" in Plex.
const minPlayableFileSizeBytes = 50 * 1024 * 1024 // 50 MB

func isPlayableMedia(name string, sizeBytes int64) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".avi":
		return !isSampleFilename(name) && sizeBytes >= minPlayableFileSizeBytes
	default:
		return false
	}
}

// IsPlayableMediaFile is the exported form of isPlayableMedia for callers
// outside this package (e.g. the pre-publish content validator).
func IsPlayableMediaFile(name string, sizeBytes int64) bool {
	return isPlayableMedia(name, sizeBytes)
}

// isSampleFilename returns true when the filename (without extension) is or
// looks like a sample clip: exactly "sample", "sample-something",
// "something-sample", etc. Mirrors the reSample logic in ranking.
func isSampleFilename(name string) bool {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))
	return base == "sample" ||
		strings.HasPrefix(base, "sample-") ||
		strings.HasPrefix(base, "sample_") ||
		strings.HasSuffix(base, "-sample") ||
		strings.HasSuffix(base, "_sample") ||
		strings.HasSuffix(base, ".sample")
}

func (db *DB) applyImportPolicies(ctx context.Context, imported ImportedNZB) ImportedNZB {
	settings := policy.DefaultSettings()
	if db != nil {
		var stored policy.Settings
		if ok, err := db.GetAppSetting(ctx, policy.SettingsKey, &stored); err == nil && ok {
			settings = policy.Merge(settings, stored)
		}
	}
	return filterImportedByPatterns(imported, settings.IgnoredPatterns)
}

func filterImportedByPatterns(imported ImportedNZB, patterns []string) ImportedNZB {
	if len(patterns) == 0 || len(imported.Files) == 0 {
		return imported
	}
	filtered := imported
	filtered.Files = make([]ImportedNZBFile, 0, len(imported.Files))
	filtered.Archives = nil
	filtered.FileCount = 0
	filtered.SegmentCount = 0
	for _, file := range imported.Files {
		if matchesIgnoredPattern(file.FileName, patterns) {
			continue
		}
		filtered.Files = append(filtered.Files, file)
		filtered.FileCount++
		filtered.SegmentCount += len(file.Segments)
	}
	// Re-detect archives from the filtered file list — clearing then rebuilding
	// ensures that files removed by the ignore patterns (e.g. .nfo) don't
	// leave orphaned volume references in the archive groups.
	filtered.Archives = DetectImportedArchives(filtered.Files)
	return filtered
}

func matchesIgnoredPattern(name string, patterns []string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(name)))
	if base == "" {
		return false
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if ok, err := filepath.Match(pattern, base); err == nil && ok {
			return true
		}
	}
	return false
}

// ClearFailedQueueItems resets all failed queue items back to 'requested' so
// they leave the history view and re-enter the search queue on the next pass.
// Returns the number of items reset.
func (db *DB) ClearFailedQueueItems(ctx context.Context) (int, error) {
	result, err := db.SQL.ExecContext(ctx, `
		UPDATE queue_items SET
			state       = $1,
			failure_reason = '',
			updated_at  = now()
		WHERE state = $2`,
		QueueRequested, QueueFailed)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// ResetStuckQueueItems resets items stuck in transitional states (fetching_nzb,
// indexing, publishing, preflight, searching, ranking) back to failed so they
// will be retried by the next monitoring pass. This runs on startup to recover
// from in-progress work that was interrupted by a process restart.
func (db *DB) ResetStuckQueueItems(ctx context.Context) (int, error) {
	result, err := db.SQL.ExecContext(ctx, `
		UPDATE queue_items SET
			state = $1,
			failure_reason = 'interrupted_by_restart',
			updated_at = now()
		WHERE state IN ($2, $3, $4, $5, $6, $7, $8)`,
		QueueFailed,
		QueueFetchingNZB, QueueIndexing, QueuePublishing,
		QueuePreflight, QueueSearching, QueueRanking, QueueSelected,
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// RecoverInterruptedDownloads resets all failed items whose failure reason is
// 'interrupted_by_restart' or 'stale_worker' and that have a selected release
// back to 'requested' so they re-enter the normal download cycle.  This runs
// once on startup to instantly recover the full backlog without the 500-item
// per-pass limit that RetryFailedQueue imposes (needed to cap Hydra calls for
// non-stale items, but irrelevant here since no Hydra call is made).
func (db *DB) RecoverInterruptedDownloads(ctx context.Context) (int, error) {
	result, err := db.SQL.ExecContext(ctx, `
		UPDATE queue_items
		SET state = $1, failure_reason = '', updated_at = now()
		WHERE state = $2
		  AND failure_reason IN ('interrupted_by_restart', 'stale_worker')
		  AND selected_release_id IS NOT NULL`,
		QueueRequested, QueueFailed,
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// ResetStaleQueueItems resets items stuck in transitional states.
// Active download states (fetching_nzb, indexing, publishing) use downloadStaleAfter
// because large files can take tens of minutes. The selected state uses
// selectedStaleAfter (20 min) because BullMQ workers may be busy. Idle
// search transitions (preflight, searching, ranking) use staleAfter (10 min).
func (db *DB) ResetStaleQueueItems(ctx context.Context, staleAfter, downloadStaleAfter, selectedStaleAfter time.Duration) (int, error) {
	now := time.Now()
	idleCutoff := now.Add(-staleAfter)
	downloadCutoff := now.Add(-downloadStaleAfter)
	selectedCutoff := now.Add(-selectedStaleAfter)
	result, err := db.SQL.ExecContext(ctx, `
		UPDATE queue_items SET
			state = $1,
			failure_reason = 'stale_worker',
			updated_at = now()
		WHERE (
			(state IN ($2, $3, $4) AND updated_at < $7)
			OR (state IN ($5, $6, $8) AND updated_at < $10)
			OR (state = $9 AND updated_at < $11)
		)`,
		QueueFailed,
		QueueFetchingNZB, QueueIndexing, QueuePublishing, // slow: download cutoff ($7)
		QueuePreflight, QueueSearching, // fast: idle cutoff ($10)
		downloadCutoff,
		QueueRanking,  // fast: idle cutoff ($10)
		QueueSelected, // medium: selected cutoff ($11)
		idleCutoff,
		selectedCutoff,
	)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (db *DB) ListSabQueueItems(ctx context.Context, category string, start, limit int) ([]SabQueueItem, int, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT li.id, li.title, li.media_type, q.state
		FROM library_items li
		JOIN queue_items q ON q.library_item_id = li.id
		WHERE q.state NOT IN ('available', 'degraded', 'failed')
		  AND q.sab_dismissed = false
		  AND ($1 = ''
		       OR ($1 = 'movies' AND li.media_type = 'movie')
		       OR ($1 = 'tv' AND li.media_type IN ('tv', 'episode'))
		       OR ($1 NOT IN ('movies', 'tv')))
		ORDER BY q.created_at ASC
		LIMIT $2 OFFSET $3`, category, limit, start)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []SabQueueItem
	for rows.Next() {
		var it SabQueueItem
		if err := rows.Scan(&it.LibraryItemID, &it.Title, &it.MediaType, &it.State); err != nil {
			return nil, 0, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var total int
	err = db.SQL.QueryRowContext(ctx, `
		SELECT count(*)
		FROM library_items li
		JOIN queue_items q ON q.library_item_id = li.id
		WHERE q.state NOT IN ('available', 'degraded', 'failed')
		  AND q.sab_dismissed = false
		  AND ($1 = ''
		       OR ($1 = 'movies' AND li.media_type = 'movie')
		       OR ($1 = 'tv' AND li.media_type IN ('tv', 'episode'))
		       OR ($1 NOT IN ('movies', 'tv')))`, category).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (db *DB) ListSabHistoryItems(ctx context.Context, category string, start, limit int) ([]SabHistoryItem, int, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT li.id, li.title, li.media_type, q.state, q.failure_reason,
		       COALESCE(q.selected_release_id, 0),
		       COALESCE((
		           SELECT SUM(nf.file_size_bytes)
		           FROM nzb_documents nd
		           JOIN nzb_files nf ON nf.nzb_document_id = nd.id
		           WHERE nd.selected_release_id = q.selected_release_id
		       ), 0)
		FROM library_items li
		JOIN queue_items q ON q.library_item_id = li.id
		WHERE q.state IN ('available', 'degraded', 'failed')
		  AND q.sab_dismissed = false
		  AND ($1 = ''
		       OR ($1 = 'movies' AND li.media_type = 'movie')
		       OR ($1 = 'tv' AND li.media_type IN ('tv', 'episode'))
		       OR ($1 NOT IN ('movies', 'tv')))
		ORDER BY q.updated_at DESC
		LIMIT $2 OFFSET $3`, category, limit, start)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []SabHistoryItem
	for rows.Next() {
		var it SabHistoryItem
		if err := rows.Scan(&it.LibraryItemID, &it.Title, &it.MediaType, &it.State, &it.FailureReason, &it.SelectedReleaseID, &it.TotalBytes); err != nil {
			return nil, 0, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var total int
	err = db.SQL.QueryRowContext(ctx, `
		SELECT count(*)
		FROM library_items li
		JOIN queue_items q ON q.library_item_id = li.id
		WHERE q.state IN ('available', 'degraded', 'failed')
		  AND q.sab_dismissed = false
		  AND ($1 = ''
		       OR ($1 = 'movies' AND li.media_type = 'movie')
		       OR ($1 = 'tv' AND li.media_type IN ('tv', 'episode'))
		       OR ($1 NOT IN ('movies', 'tv')))`, category).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// DismissSabItems marks queue items as dismissed from the SABnzbd history/queue
// view. Called when Radarr/Sonarr sends mode=history&name=delete or
// mode=queue&name=delete. Dismissed items are excluded from future polls
// without altering queue state or triggering any workflow transitions.
func (db *DB) DismissSabItems(ctx context.Context, libraryItemIDs []int64) error {
	if len(libraryItemIDs) == 0 {
		return nil
	}
	// Build $1, $2, ... placeholders
	placeholders := make([]string, len(libraryItemIDs))
	args := make([]any, len(libraryItemIDs))
	for i, id := range libraryItemIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	query := fmt.Sprintf(`
		UPDATE queue_items SET sab_dismissed = true
		WHERE library_item_id IN (%s)`, strings.Join(placeholders, ","))
	_, err := db.SQL.ExecContext(ctx, query, args...)
	return err
}
