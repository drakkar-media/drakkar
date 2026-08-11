-- Persists the passive-resume dispatch sweep's escalating per-item backoff
-- (previously an in-memory-only map on workflow.Service, reset to zero by
-- every process restart/deploy). Confirmed live 2026-08-11: a small cluster
-- of library items with hundreds of rejected release_candidates (Alien:
-- Earth 689/797, Chicago P.D. S03E22 433/434) kept getting re-dispatched
-- every ~1-2 minutes, flooding NZBHydra's download history indefinitely,
-- because this session's own repeated redeploys kept resetting the
-- in-memory backoff counter before it could climb to its longer tiers.
alter table queue_items add column if not exists dispatch_attempt_count integer not null default 0;
alter table queue_items add column if not exists dispatch_backoff_until timestamptz;
