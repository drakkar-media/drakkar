package database

import (
	"context"
	"fmt"
)

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
// across every entry of selectedReleaseID. Kept as a cheap sanity signal, but
// NOT sufficient on its own to detect whether a repair actually changed
// anything: aggregateRARVolumeEntries' own diff<=16 reconciliation forces the
// entry's total size to match regardless of whether an individual volume's
// boundary was computed correctly, so the exact corruption this repair fixes
// -- a wrong split between two volumes' lengths -- routinely leaves the
// grand total completely unchanged. Use SnapshotArchiveRangesForRelease for
// real before/after change detection.
func (db *DB) SumVirtualFileSizeForRelease(ctx context.Context, selectedReleaseID int64) (int64, error) {
	var total int64
	err := db.SQL.QueryRowContext(ctx, `
		select coalesce(sum(size_bytes), 0)
		from virtual_files
		where selected_release_id = $1`, selectedReleaseID,
	).Scan(&total)
	return total, err
}

// SnapshotArchiveRangesForRelease returns a stable, order-independent
// representation of every archive_ranges row for selectedReleaseID's
// archives, keyed by (entry path, volume index) rather than row id --
// ImportSelectedReleaseNZB deletes and recreates these rows with fresh ids on
// every repair, so id-based comparison would always report "changed" even
// when nothing about the actual byte layout moved. Compare two snapshots with
// slices.Equal after sorting (both are already sorted here) to detect
// whether a repair actually altered any volume's offset or length.
func (db *DB) SnapshotArchiveRangesForRelease(ctx context.Context, selectedReleaseID int64) ([]string, error) {
	rows, err := db.SQL.QueryContext(ctx, `
		select ae.path, av.volume_index, ar.entry_offset, ar.archive_offset, ar.length_bytes
		from archive_ranges ar
		join archive_entries ae on ae.id = ar.archive_entry_id
		join archive_volumes av on av.id = ar.archive_volume_id
		join archives a on a.id = ae.archive_id
		where a.selected_release_id = $1
		order by ae.path, av.volume_index`, selectedReleaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var path string
		var volumeIndex int
		var entryOffset, archiveOffset, lengthBytes int64
		if err := rows.Scan(&path, &volumeIndex, &entryOffset, &archiveOffset, &lengthBytes); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s|%d|%d|%d|%d", path, volumeIndex, entryOffset, archiveOffset, lengthBytes))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
