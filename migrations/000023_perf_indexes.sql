-- Fix invalid nzb_segment_id index and add missing library_item_id index to
-- prevent full sequential scans during cascade deletes (which caused 100% CPU
-- when many items were re-searched simultaneously).
DROP INDEX IF EXISTS idx_virtual_file_ranges_nzb_segment_id;
CREATE INDEX IF NOT EXISTS idx_virtual_file_ranges_nzb_segment_id ON virtual_file_ranges (nzb_segment_id);
CREATE INDEX IF NOT EXISTS idx_selected_releases_library_item_id ON selected_releases (library_item_id);
