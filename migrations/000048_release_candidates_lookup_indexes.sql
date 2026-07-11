-- Three release_candidates lookups were doing full (parallel) sequential
-- scans of an unbounded, currently 400k+ row table because nothing indexed
-- the columns they filter on: blocklist enrichment by external_url (blocklist
-- page N+1, ~740ms/row live), blocklist enrichment by indexer+size+date
-- signature (same page), and the periodic stale-candidate prune job filtering
-- on selected=false/created_at (~50s+ per run live). All three are purely
-- additive indexes -- no behavior change, just faster lookups.

create index if not exists idx_release_candidates_external_url
    on public.release_candidates (external_url)
    where external_url <> '';

-- posted_at::date depends on the session TimeZone (this DB's is Europe/Amsterdam),
-- but the Go-side blocklist key was built from postedAt.UTC().Format(...)
-- (blocklistReleaseSignatureKey) -- a real, pre-existing mismatch that could
-- silently miss matches for releases posted near local midnight. Bucketing by
-- UTC here (both in this index and the query it supports) fixes that and,
-- as a fixed-offset zone, is also the only cast Postgres will allow in an
-- index expression (a named zone like 'Europe/Amsterdam' is not immutable).
create index if not exists idx_release_candidates_signature
    on public.release_candidates (
        lower(trim(indexer_name)),
        (coalesce(size_bytes, 0) / (1024 * 1024)),
        ((posted_at at time zone 'UTC')::date)
    );

create index if not exists idx_release_candidates_prune
    on public.release_candidates (created_at)
    where selected = false;
