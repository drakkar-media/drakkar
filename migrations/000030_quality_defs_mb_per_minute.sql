-- Rename quality_definitions size columns to reflect their actual semantics.
-- Values are MB per minute (Sonarr/Radarr convention), not total file size.
-- The old names were misleading; the column values are unchanged.
ALTER TABLE quality_definitions
    RENAME COLUMN min_size_mb TO min_mb_per_minute;
ALTER TABLE quality_definitions
    RENAME COLUMN max_size_mb TO max_mb_per_minute;
