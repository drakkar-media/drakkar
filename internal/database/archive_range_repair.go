package database

import "context"

// ArchiveRangeRepairCandidate identifies a multi-volume RAR archive whose
// entries/ranges may have been computed by the pre-fix inspectRARArchive
// (see reconcileStoreMethodSize's ordering bug): volume 0's own,
// header-declared data size could have been overwritten with the entry's
// cross-volume total before aggregateRARVolumeEntries used it, letting its
// safety clamp pass through the volume's full physical capacity -- including
// a trailing RAR5 "QO" (Quick Open) service block -- as if it were video
// data. Repair re-runs inspection against live NNTP data and is a no-op for
// an archive that was never affected.
type ArchiveRangeRepairCandidate struct {
	ArchiveID         int64
	SelectedReleaseID int64
}

// archiveRangeRepairCandidatesQuery selects multi-volume RAR archives in
// "supported" status. A single-volume archive can't exhibit this bug
// (reconcileStoreMethodSize only runs when len(archive.Volumes) > 1), so
// excluding those up front keeps the sweep from touching archives that were
// never at risk.
const archiveRangeRepairCandidatesQuery = `
	select a.id, a.selected_release_id
	from archives a
	where a.kind = 'rar' and a.status = 'supported'
	  and (select count(*) from archive_volumes av where av.archive_id = a.id) > 1
`

// ListArchiveRangeRepairCandidatesPage returns one stable keyset page of
// repair candidates for a bounded sweep step. archiveID ordering (rather
// than a mutable timestamp) keeps later pages from being reordered by
// repairs the sweep itself performs. throughArchiveID freezes the sweep's
// population the same way DeepHealthSweepUpperBound does for the health
// check, while new archives keep arriving from ongoing imports.
func (db *DB) ListArchiveRangeRepairCandidatesPage(ctx context.Context, afterArchiveID, throughArchiveID int64, limit int) ([]ArchiveRangeRepairCandidate, error) {
	if limit <= 0 {
		return []ArchiveRangeRepairCandidate{}, nil
	}
	rows, err := db.SQL.QueryContext(ctx, `
		select * from (`+archiveRangeRepairCandidatesQuery+`) candidates
		where id > $1 and id <= $2
		order by id asc
		limit $3`, afterArchiveID, throughArchiveID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchiveRangeRepairCandidate
	for rows.Next() {
		var c ArchiveRangeRepairCandidate
		if err := rows.Scan(&c.ArchiveID, &c.SelectedReleaseID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ArchiveRangeRepairSweepUpperBound returns a cheap, stable archive-id
// ceiling for a new repair sweep, mirroring DeepHealthSweepUpperBound: it
// intentionally includes archives outside the candidate query (a single
// volume, or a different kind/status), since the page query itself filters
// eligibility and a plain max(id) avoids a second pass over the join.
func (db *DB) ArchiveRangeRepairSweepUpperBound(ctx context.Context) (int64, error) {
	var upperBound int64
	err := db.SQL.QueryRowContext(ctx, `select coalesce(max(id), 0) from archives`).Scan(&upperBound)
	return upperBound, err
}

// SumVirtualFileSizeForRelease returns the total virtual_files.size_bytes
// across every entry of selectedReleaseID, used by the archive-range repair
// task to detect whether re-inspection actually changed anything (0 if the
// release currently has no virtual files, e.g. mid-repair).
func (db *DB) SumVirtualFileSizeForRelease(ctx context.Context, selectedReleaseID int64) (int64, error) {
	var total int64
	err := db.SQL.QueryRowContext(ctx, `
		select coalesce(sum(size_bytes), 0)
		from virtual_files
		where selected_release_id = $1`, selectedReleaseID,
	).Scan(&total)
	return total, err
}
