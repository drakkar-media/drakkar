ALTER TABLE quality_profiles
    ADD COLUMN IF NOT EXISTS minimum_upgrade_custom_format_score INTEGER NOT NULL DEFAULT 0;

ALTER TABLE release_candidates
    ADD COLUMN IF NOT EXISTS custom_format_score INTEGER NOT NULL DEFAULT 0;
