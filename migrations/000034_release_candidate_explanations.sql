ALTER TABLE release_candidates
    ADD COLUMN explanations text[] NOT NULL DEFAULT '{}';
