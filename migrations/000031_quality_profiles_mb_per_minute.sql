-- Align quality_profiles storage naming with the existing MB/minute semantics.
ALTER TABLE quality_profiles
    RENAME COLUMN min_size_mb TO min_mb_per_minute;

ALTER TABLE quality_profiles
    RENAME COLUMN max_size_mb TO max_mb_per_minute;
