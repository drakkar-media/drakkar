-- Add segment_byte_start to virtual_file_ranges: byte offset within the decoded
-- NZB segment where this span's virtual-file content begins. Non-zero only for
-- the first segment of each RAR volume (which contains the RAR header before
-- the actual file data). Zero for all direct_nzb spans and for stored_rar spans
-- that start on a segment boundary after the archive header.
ALTER TABLE virtual_file_ranges ADD COLUMN segment_byte_start bigint NOT NULL DEFAULT 0;

-- Back-fill for existing stored_rar virtual files.
-- Formula: archive_byte_position_of_range_start - decoded_start_offset
--   where archive_byte_position = ar.archive_offset + (vfr.range_start - ar.entry_offset)
UPDATE virtual_file_ranges vfr
SET segment_byte_start = ar.archive_offset + (vfr.range_start - ar.entry_offset) - ns.decoded_start_offset
FROM nzb_segments ns,
     virtual_files vf,
     archives a,
     archive_entries ae,
     archive_ranges ar
WHERE vfr.nzb_segment_id = ns.id
  AND vfr.virtual_file_id = vf.id
  AND vf.reader_kind = 'stored_rar'
  AND a.selected_release_id = vf.selected_release_id
  AND ae.archive_id = a.id
  AND ae.path = vf.file_name
  AND ar.archive_entry_id = ae.id
  AND vfr.range_start >= ar.entry_offset
  AND vfr.range_start < ar.entry_offset + ar.length_bytes;
