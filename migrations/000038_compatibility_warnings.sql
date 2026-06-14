ALTER TABLE release_candidates
    ADD COLUMN compatibility_warnings text[] NOT NULL DEFAULT '{}';
