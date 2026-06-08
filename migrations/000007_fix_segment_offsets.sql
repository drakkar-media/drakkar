-- Fix segment decoded offsets: prior factor was 0.74, correct factor is 0.97.
-- Only applies when the stored ratio is still at the old 0.74 factor.
-- Checks the first segment's ratio to detect whether migration is needed.

DO $$
DECLARE
    sample_ratio numeric;
BEGIN
    SELECT ROUND(100.0 * (decoded_end_offset - decoded_start_offset) / NULLIF(encoded_size_bytes, 0), 1)
    INTO sample_ratio
    FROM nzb_segments
    WHERE encoded_size_bytes > 0
    LIMIT 1;

    IF sample_ratio IS NULL OR sample_ratio > 80 THEN
        -- Already at 0.97 factor (ratio ~97) or no segments — skip.
        RAISE NOTICE 'Segment offset migration skipped (current ratio: %)', sample_ratio;
        RETURN;
    END IF;

    RAISE NOTICE 'Applying segment offset correction (current ratio: %)', sample_ratio;

    UPDATE nzb_segments SET
        decoded_start_offset = ROUND(decoded_start_offset * 0.97 / 0.74),
        decoded_end_offset   = ROUND(decoded_end_offset   * 0.97 / 0.74)
    WHERE decoded_end_offset > 0;

    UPDATE virtual_files SET
        size_bytes = ROUND(size_bytes * 0.97 / 0.74)
    WHERE size_bytes > 0;

    UPDATE virtual_file_ranges SET
        range_start = ROUND(range_start * 0.97 / 0.74),
        range_end   = ROUND(range_end   * 0.97 / 0.74)
    WHERE range_end > 0;
END $$;
