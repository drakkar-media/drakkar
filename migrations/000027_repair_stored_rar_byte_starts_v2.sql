-- Repair stored_rar VFRs where migrations 000025 and 000026 both failed.
-- Root cause: migration 000007 scaled virtual_files.size_bytes by factor 0.97/0.74 but
-- left archive_entries.size_bytes unscaled, so the ae.size_bytes = vf.size_bytes join
-- in migration 000026 silently found no match for older releases.
--
-- Fix: drop size from the JOIN; use path/size as ORDER BY preferences only.
-- Restricts to range_start = 0 (span-1 VFRs) since only span 1 needs a non-zero
-- segment_byte_start equal to the RAR header size (ar.archive_offset).
WITH candidates AS (
    SELECT DISTINCT ON (vfr.id)
        vfr.id,
        ar.archive_offset AS new_sbs
    FROM virtual_file_ranges vfr
    JOIN nzb_segments ns      ON ns.id  = vfr.nzb_segment_id
    JOIN virtual_files vf     ON vf.id  = vfr.virtual_file_id
    JOIN archives a            ON a.selected_release_id = vf.selected_release_id
    JOIN archive_entries ae    ON ae.archive_id = a.id
    JOIN archive_ranges ar     ON ar.archive_entry_id = ae.id
                               AND ar.entry_offset = 0
    WHERE vf.reader_kind            = 'stored_rar'
      AND vfr.segment_byte_start    = 0
      AND vfr.range_start           = 0
      AND ns.decoded_start_offset   = 0
      AND ar.archive_offset         > 0
    ORDER BY vfr.id,
             (ae.path = vf.file_name)        DESC,
             (ae.size_bytes = vf.size_bytes) DESC,
             ar.archive_offset               DESC
)
UPDATE virtual_file_ranges vfr
SET    segment_byte_start = c.new_sbs
FROM   candidates c
WHERE  vfr.id = c.id;
