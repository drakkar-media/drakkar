package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (db *DB) ListLibraryItems(ctx context.Context) ([]LibraryItemSummary, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select
			li.id,
			li.media_type,
			li.title,
			li.available,
			li.requested_at,
			coalesce(q.state, ''),
			coalesce(q.failure_reason, ''),
			q.selected_release_id
		from library_items li
		left join lateral (
			select state, failure_reason, selected_release_id
			from queue_items
			where library_item_id = li.id
			order by id desc limit 1
		) q on true
		order by li.requested_at desc, li.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LibraryItemSummary
	for rows.Next() {
		var (
			item            LibraryItemSummary
			selectedRelease sql.NullInt64
			queueState      string
		)
		if err := rows.Scan(
			&item.ID,
			&item.MediaType,
			&item.Title,
			&item.Available,
			&item.RequestedAt,
			&queueState,
			&item.FailureReason,
			&selectedRelease,
		); err != nil {
			return nil, err
		}
		item.QueueState = QueueState(queueState)
		if selectedRelease.Valid {
			value := selectedRelease.Int64
			item.SelectedReleaseID = &value
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) ListReleaseSummaries(ctx context.Context, libraryItemID int64) ([]ReleaseSummary, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select
			sr.id,
			rc.id,
			rc.library_item_id,
			rc.title,
			rc.external_url,
			rc.indexer_name,
			rc.size_bytes,
			coalesce(rc.posted_at, to_timestamp(0)),
			rc.score,
			coalesce(rc.custom_format_score, 0),
			rc.selected,
			rc.rejected,
			rc.reject_reason,
			rc.failure_count,
			rc.last_failure_reason,
			rc.explanations,
			rc.compatibility_warnings,
			coalesce((select count(*) from archives a where a.selected_release_id = sr.id), 0),
			coalesce((select count(*) from archive_volumes av join archives a on a.id = av.archive_id where a.selected_release_id = sr.id), 0),
			coalesce((select string_agg(distinct a.status, ',' order by a.status) from archives a where a.selected_release_id = sr.id), ''),
			coalesce((select string_agg(distinct a.reject_reason, ',' order by a.reject_reason) from archives a where a.selected_release_id = sr.id and a.reject_reason <> ''), ''),
			coalesce((select count(*) from virtual_files vf where vf.selected_release_id = sr.id), 0),
			rc.created_at,
			n.id,
			coalesce(n.file_name, '')
		from release_candidates rc
		left join selected_releases sr on sr.release_candidate_id = rc.id
		left join lateral (
			select id, file_name
			from nzb_documents
			where selected_release_id = sr.id
			order by id desc limit 1
		) n on sr.id is not null
		where rc.library_item_id = $1
		order by rc.selected desc, rc.score desc, rc.created_at asc, rc.id asc`, libraryItemID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Pass 1: scan all rows, collecting IDs for bulk loads (O-01: eliminate N+1).
	type partialReleaseSummary struct {
		item              ReleaseSummary
		selectedReleaseID sql.NullInt64
		nzbDocument       sql.NullInt64
	}
	var partials []partialReleaseSummary
	var selectedReleaseIDs []int64
	var candidateIDs []int64

	for rows.Next() {
		var p partialReleaseSummary
		if err := rows.Scan(
			&p.selectedReleaseID,
			&p.item.ReleaseCandidateID,
			&p.item.LibraryItemID,
			&p.item.Title,
			&p.item.ExternalURL,
			&p.item.IndexerName,
			&p.item.SizeBytes,
			&p.item.PostedAt,
			&p.item.Score,
			&p.item.CustomFormatScore,
			&p.item.Selected,
			&p.item.Rejected,
			&p.item.RejectReason,
			&p.item.FailureCount,
			&p.item.LastFailureReason,
			pgTextArrayScan(&p.item.Explanations),
			pgTextArrayScan(&p.item.CompatibilityWarnings),
			&p.item.ArchiveCount,
			&p.item.ArchiveVolumeCount,
			&p.item.ArchiveStatuses,
			&p.item.ArchiveRejects,
			&p.item.VirtualFileCount,
			&p.item.CreatedAt,
			&p.nzbDocument,
			&p.item.NZBFileName,
		); err != nil {
			return nil, err
		}
		if p.selectedReleaseID.Valid {
			selectedReleaseIDs = append(selectedReleaseIDs, p.selectedReleaseID.Int64)
		}
		candidateIDs = append(candidateIDs, p.item.ReleaseCandidateID)
		partials = append(partials, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Pass 2: bulk-load archives and failed attempts (2 queries instead of 2N).
	archivesByRelease, err := db.bulkLoadArchivesByRelease(ctx, selectedReleaseIDs)
	if err != nil {
		return nil, err
	}
	failedByCandidate, err := db.bulkLoadFailedAttemptsByCandidate(ctx, candidateIDs)
	if err != nil {
		return nil, err
	}

	// Pass 3: merge into final result slice.
	var out []ReleaseSummary
	for _, p := range partials {
		item := p.item
		if p.selectedReleaseID.Valid {
			item.SelectedReleaseID = p.selectedReleaseID.Int64
			item.Archives = archivesByRelease[p.selectedReleaseID.Int64]
		}
		item.FailedAttempts = failedByCandidate[item.ReleaseCandidateID]
		if p.nzbDocument.Valid {
			value := p.nzbDocument.Int64
			item.NZBDocumentID = &value
		}
		item.Explanations = releaseSummaryExplanations(item)
		out = append(out, item)
	}
	return out, nil
}

func releaseSummaryExplanations(item ReleaseSummary) []string {
	out := append([]string{}, item.Explanations...)
	if item.Selected {
		out = append(out, "Currently selected for this library item.")
	}
	if item.Rejected && strings.TrimSpace(item.RejectReason) != "" {
		if strings.HasPrefix(item.RejectReason, "blocklist:") {
			out = append(out, "Rejected by a release filtering rule: "+strings.TrimPrefix(item.RejectReason, "blocklist:"))
		} else {
			out = append(out, "Rejected: "+item.RejectReason)
		}
	}
	if item.FailureCount > 0 {
		message := fmt.Sprintf("Previously failed %d time(s).", item.FailureCount)
		if strings.TrimSpace(item.LastFailureReason) != "" {
			message += " Latest failure: " + item.LastFailureReason + "."
		}
		out = append(out, message)
	}
	if strings.TrimSpace(item.ArchiveRejects) != "" {
		out = append(out, "Archive inspection rejected content: "+item.ArchiveRejects)
	}
	if len(out) == 0 && !item.Rejected && item.FailureCount == 0 && strings.TrimSpace(item.ArchiveRejects) == "" && !item.Selected {
		out = append(out, "No stored rejections or failed attempts for this candidate.")
	}
	return out
}

func (db *DB) listFailedReleaseAttempts(ctx context.Context, releaseCandidateID int64) ([]FailedReleaseAttempt, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select reason, created_at
		from failed_releases
		where release_candidate_id = $1
		order by created_at desc, id desc`, releaseCandidateID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FailedReleaseAttempt
	for rows.Next() {
		var item FailedReleaseAttempt
		if err := rows.Scan(&item.Reason, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (db *DB) listReleaseArchives(ctx context.Context, selectedReleaseID int64) ([]ReleaseArchiveSummary, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select
			a.id,
			a.kind,
			a.status,
			a.reject_reason,
			coalesce((select count(*) from archive_volumes av where av.archive_id = a.id), 0)
		from archives a
		where a.selected_release_id = $1
		order by a.id asc`, selectedReleaseID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type archiveRow struct {
		id int64
		ReleaseArchiveSummary
	}
	var items []archiveRow
	for rows.Next() {
		var item archiveRow
		if err := rows.Scan(&item.id, &item.Kind, &item.Status, &item.RejectReason, &item.VolumeCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range items {
		entryRows, err := db.SQL.QueryContext(ctx, `
			select
				path,
				size_bytes,
				packed_size_bytes,
				compression_method,
				encrypted,
				solid,
				source_volume_index,
				source_archive_offset
			from archive_entries
			where archive_id = $1
			order by path asc`, items[i].id,
		)
		if err != nil {
			return nil, err
		}
		for entryRows.Next() {
			var entry ReleaseArchiveEntry
			if err := entryRows.Scan(
				&entry.Path,
				&entry.SizeBytes,
				&entry.PackedSizeBytes,
				&entry.CompressionMethod,
				&entry.Encrypted,
				&entry.Solid,
				&entry.SourceVolumeIndex,
				&entry.SourceArchiveOffset,
			); err != nil {
				entryRows.Close()
				return nil, err
			}
			items[i].Entries = append(items[i].Entries, entry)
		}
		if err := entryRows.Close(); err != nil {
			return nil, err
		}
	}

	out := make([]ReleaseArchiveSummary, 0, len(items))
	for _, item := range items {
		out = append(out, item.ReleaseArchiveSummary)
	}
	return out, nil
}

// buildIntInClause builds a "$1,$2,..." placeholder string and matching args slice for an IN clause.
func buildIntInClause(ids []int64) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

// bulkLoadArchivesByRelease loads all archives and their entries for multiple
// selected_release_ids using two queries instead of 2N (O-01).
func (db *DB) bulkLoadArchivesByRelease(ctx context.Context, selectedReleaseIDs []int64) (map[int64][]ReleaseArchiveSummary, error) {
	if len(selectedReleaseIDs) == 0 {
		return nil, nil
	}
	ph, args := buildIntInClause(selectedReleaseIDs)
	archRows, err := db.SQL.QueryContext(ctx, fmt.Sprintf(`
		select a.id, a.selected_release_id, a.kind, a.status, a.reject_reason,
		       coalesce((select count(*) from archive_volumes av where av.archive_id = a.id), 0)
		from archives a
		where a.selected_release_id in (%s)
		order by a.selected_release_id, a.id asc`, ph), args...)
	if err != nil {
		return nil, err
	}
	defer archRows.Close()

	type archItem struct {
		id        int64
		releaseID int64
		ReleaseArchiveSummary
	}
	var archList []archItem
	archIdx := map[int64]int{} // archive id → index in archList
	var archIDs []int64

	for archRows.Next() {
		var item archItem
		if err := archRows.Scan(&item.id, &item.releaseID, &item.Kind, &item.Status, &item.RejectReason, &item.VolumeCount); err != nil {
			return nil, err
		}
		archIdx[item.id] = len(archList)
		archIDs = append(archIDs, item.id)
		archList = append(archList, item)
	}
	if err := archRows.Err(); err != nil {
		return nil, err
	}

	if len(archIDs) > 0 {
		ph2, args2 := buildIntInClause(archIDs)
		entryRows, err := db.SQL.QueryContext(ctx, fmt.Sprintf(`
			select archive_id, path, size_bytes, packed_size_bytes,
			       compression_method, encrypted, solid,
			       source_volume_index, source_archive_offset
			from archive_entries
			where archive_id in (%s)
			order by archive_id, path asc`, ph2), args2...)
		if err != nil {
			return nil, err
		}
		defer entryRows.Close()
		for entryRows.Next() {
			var archiveID int64
			var entry ReleaseArchiveEntry
			if err := entryRows.Scan(
				&archiveID, &entry.Path, &entry.SizeBytes, &entry.PackedSizeBytes,
				&entry.CompressionMethod, &entry.Encrypted, &entry.Solid,
				&entry.SourceVolumeIndex, &entry.SourceArchiveOffset,
			); err != nil {
				return nil, err
			}
			if idx, ok := archIdx[archiveID]; ok {
				archList[idx].Entries = append(archList[idx].Entries, entry)
			}
		}
		if err := entryRows.Err(); err != nil {
			return nil, err
		}
	}

	result := make(map[int64][]ReleaseArchiveSummary, len(selectedReleaseIDs))
	for _, item := range archList {
		result[item.releaseID] = append(result[item.releaseID], item.ReleaseArchiveSummary)
	}
	return result, nil
}

// bulkLoadFailedAttemptsByCandidate loads failed attempts for multiple release
// candidate IDs in a single query (O-01).
func (db *DB) bulkLoadFailedAttemptsByCandidate(ctx context.Context, candidateIDs []int64) (map[int64][]FailedReleaseAttempt, error) {
	if len(candidateIDs) == 0 {
		return nil, nil
	}
	ph, args := buildIntInClause(candidateIDs)
	rows, err := db.SQL.QueryContext(ctx, fmt.Sprintf(`
		select release_candidate_id, reason, created_at
		from failed_releases
		where release_candidate_id in (%s)
		order by release_candidate_id, created_at desc, id desc`, ph), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64][]FailedReleaseAttempt)
	for rows.Next() {
		var candidateID int64
		var item FailedReleaseAttempt
		if err := rows.Scan(&candidateID, &item.Reason, &item.CreatedAt); err != nil {
			return nil, err
		}
		result[candidateID] = append(result[candidateID], item)
	}
	return result, rows.Err()
}
