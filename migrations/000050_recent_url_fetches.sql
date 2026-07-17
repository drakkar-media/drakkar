-- Persists "this external URL was dispatched to the indexer at time T",
-- surviving process restarts. workflow.Service's in-memory recentURLHits map
-- (the primary 30-min per-URL fetch cooldown) resets to empty on every
-- restart, so a release fetch interrupted mid-flight by a redeploy could be
-- re-dispatched with zero cooldown protection once the process comes back
-- up. This table is a persisted backstop checked alongside that in-memory
-- cooldown for exactly that restart window.
create table if not exists recent_url_fetches (
    external_url text primary key,
    dispatched_at timestamptz not null default now()
);
