-- ── Grab history ───────────────────────────────────────────────────────────
-- Records every time a release candidate is selected (grabbed) for download.
-- Mirrors Radarr/Sonarr's history table for debugging and auditing.
CREATE TABLE IF NOT EXISTS grab_history (
    id               bigserial PRIMARY KEY,
    library_item_id  bigint NOT NULL REFERENCES library_items(id) ON DELETE CASCADE,
    release_candidate_id bigint REFERENCES release_candidates(id) ON DELETE SET NULL,
    title            text NOT NULL DEFAULT '',
    indexer_name     text NOT NULL DEFAULT '',
    score            integer NOT NULL DEFAULT 0,
    resolution       text NOT NULL DEFAULT '',
    grabbed_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS grab_history_library_item_idx ON grab_history (library_item_id, grabbed_at DESC);

-- ── Quality profile enhancements ───────────────────────────────────────────
-- cutoff_resolution: once the item has a release AT or ABOVE this resolution,
-- stop upgrading it (mirrors Sonarr/Radarr cutoff concept).
ALTER TABLE quality_profiles
    ADD COLUMN IF NOT EXISTS cutoff_resolution text NOT NULL DEFAULT '';

-- minimum_age_hours: don't grab releases posted less than N hours ago.
-- Mirrors Sonarr/Radarr delay profiles (simplified to per-profile).
ALTER TABLE quality_profiles
    ADD COLUMN IF NOT EXISTS minimum_age_hours integer NOT NULL DEFAULT 0;

-- ── TV show monitoring mode ────────────────────────────────────────────────
-- Controls which episodes Drakkar will try to download for a show.
-- Values: 'all' | 'future' | 'missing' | 'recent' | 'pilot' | 'none'
ALTER TABLE tv_shows
    ADD COLUMN IF NOT EXISTS monitoring_mode text NOT NULL DEFAULT 'all';

-- ── Custom formats ─────────────────────────────────────────────────────────
-- User-defined scoring rules applied on top of the quality profile.
-- Each format adds/subtracts points when its regex matches a release title.
CREATE TABLE IF NOT EXISTS custom_formats (
    id      bigserial PRIMARY KEY,
    name    text NOT NULL,
    pattern text NOT NULL,
    score   integer NOT NULL DEFAULT 0,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- ── Release candidate resolution ───────────────────────────────────────────
-- Store detected resolution on each candidate so cutoff checks don't need
-- to re-parse titles. Populated by ReplaceSearchCandidates.
ALTER TABLE release_candidates
    ADD COLUMN IF NOT EXISTS resolution text NOT NULL DEFAULT '';
