-- Allow quality profiles to enable automatic upgrade searches for available items.
-- When enabled, items already downloaded at a sub-optimal quality will be
-- re-searched periodically to find a higher-quality release.
ALTER TABLE quality_profiles
    ADD COLUMN IF NOT EXISTS allow_upgrade bool NOT NULL DEFAULT false;
