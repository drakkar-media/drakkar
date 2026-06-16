-- Migration: replace nzb_segments + virtual_file_ranges with inline segment data.
-- Adds message_ids, decoded_segment_size, last_decoded_size to nzb_files and
-- nzb_file_id, segment_byte_offset to virtual_files, then populates them from
-- the old tables and finally drops the old tables.

-- Step 1: Add new columns to nzb_files.
ALTER TABLE nzb_files
    ADD COLUMN IF NOT EXISTS message_ids TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS decoded_segment_size BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_decoded_size BIGINT NOT NULL DEFAULT 0;

-- Step 2: Add new columns to virtual_files.
ALTER TABLE virtual_files
    ADD COLUMN IF NOT EXISTS nzb_file_id BIGINT REFERENCES nzb_files(id),
    ADD COLUMN IF NOT EXISTS segment_byte_offset BIGINT NOT NULL DEFAULT 0;

-- Step 3: Populate nzb_files.message_ids from nzb_segments (for existing rows).
UPDATE nzb_files nf SET
    message_ids = sub.ids,
    decoded_segment_size = sub.first_size,
    last_decoded_size = sub.last_size
FROM (
    SELECT ns.nzb_file_id,
        ARRAY_AGG(ns.message_id ORDER BY ns.segment_number) AS ids,
        (SELECT ns2.decoded_end_offset - ns2.decoded_start_offset FROM nzb_segments ns2
         WHERE ns2.nzb_file_id = ns.nzb_file_id ORDER BY ns2.segment_number ASC LIMIT 1) AS first_size,
        (SELECT ns2.decoded_end_offset - ns2.decoded_start_offset FROM nzb_segments ns2
         WHERE ns2.nzb_file_id = ns.nzb_file_id ORDER BY ns2.segment_number DESC LIMIT 1) AS last_size
    FROM nzb_segments ns
    GROUP BY ns.nzb_file_id
) sub
WHERE nf.id = sub.nzb_file_id AND array_length(nf.message_ids, 1) IS NULL;

-- Step 4: Populate virtual_files.nzb_file_id for direct_nzb entries.
UPDATE virtual_files vf SET nzb_file_id = sub.nzb_file_id, segment_byte_offset = 0
FROM (
    SELECT DISTINCT ON (vfr.virtual_file_id) vfr.virtual_file_id, ns.nzb_file_id
    FROM virtual_file_ranges vfr
    JOIN nzb_segments ns ON ns.id = vfr.nzb_segment_id
    ORDER BY vfr.virtual_file_id, vfr.range_start ASC
) sub
WHERE vf.id = sub.virtual_file_id AND vf.reader_kind = 'direct_nzb' AND vf.nzb_file_id IS NULL;

-- Step 5: Populate virtual_files.nzb_file_id + segment_byte_offset for stored_rar entries.
UPDATE virtual_files vf SET
    nzb_file_id = ns.nzb_file_id,
    segment_byte_offset = ns.decoded_start_offset + first_vfr.segment_byte_start
FROM (
    SELECT DISTINCT ON (vfr.virtual_file_id) vfr.virtual_file_id, vfr.nzb_segment_id, vfr.segment_byte_start
    FROM virtual_file_ranges vfr
    ORDER BY vfr.virtual_file_id, vfr.range_start ASC
) first_vfr
JOIN nzb_segments ns ON ns.id = first_vfr.nzb_segment_id
WHERE vf.id = first_vfr.virtual_file_id AND vf.reader_kind = 'stored_rar' AND vf.nzb_file_id IS NULL;

-- Step 6: Drop the old tables (idempotent).
DROP TABLE IF EXISTS virtual_file_ranges;
DROP TABLE IF EXISTS nzb_segments;
