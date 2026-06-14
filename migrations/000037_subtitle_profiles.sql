-- Subtitle profiles store named language/preference sets for subtitle acquisition.
-- A library item can be assigned a specific subtitle profile; otherwise the
-- system falls back to the global subtitle settings.
CREATE TABLE IF NOT EXISTS subtitle_profiles (
    id                    bigserial   PRIMARY KEY,
    name                  text        NOT NULL UNIQUE,
    languages             text[]      NOT NULL DEFAULT '{}',
    prefer_hearing_impaired boolean   NOT NULL DEFAULT false,
    require_exact_language  boolean   NOT NULL DEFAULT false,
    is_default            boolean     NOT NULL DEFAULT false,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- Only one default at a time.
CREATE UNIQUE INDEX IF NOT EXISTS idx_subtitle_profiles_default ON subtitle_profiles (is_default) WHERE is_default = true;
