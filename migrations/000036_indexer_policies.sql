-- Per-indexer scoring policy.
-- Operators can assign a static score modifier to releases from a named indexer
-- so that consistently good (or bad) indexers are ranked higher or lower.
CREATE TABLE IF NOT EXISTS indexer_policies (
    id             bigserial   PRIMARY KEY,
    indexer_name   text        NOT NULL UNIQUE,
    score_modifier integer     NOT NULL DEFAULT 0,
    enabled        boolean     NOT NULL DEFAULT true,
    note           text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_indexer_policies_enabled ON indexer_policies (enabled);
