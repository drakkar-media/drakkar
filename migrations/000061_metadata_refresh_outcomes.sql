-- Provider refresh success is independent of optional metadata being NULL.
-- Failed/skipped attempts retain detail and remain eligible for later retries.
alter table movies
    add column if not exists metadata_refresh_attempted_at timestamptz,
    add column if not exists metadata_refresh_status text,
    add column if not exists metadata_refresh_error text;

alter table tv_shows
    add column if not exists metadata_refresh_attempted_at timestamptz,
    add column if not exists metadata_refresh_status text,
    add column if not exists metadata_refresh_error text;

do $$
begin
    if not exists (
        select 1
        from pg_constraint
        where conname = 'movies_metadata_refresh_status_check'
          and conrelid = 'movies'::regclass
    ) then
        alter table movies
            add constraint movies_metadata_refresh_status_check
            check (metadata_refresh_status in ('success', 'failed', 'skipped'));
    end if;

    if not exists (
        select 1
        from pg_constraint
        where conname = 'tv_shows_metadata_refresh_status_check'
          and conrelid = 'tv_shows'::regclass
    ) then
        alter table tv_shows
            add constraint tv_shows_metadata_refresh_status_check
            check (metadata_refresh_status in ('success', 'failed', 'skipped'));
    end if;
end
$$;

create index if not exists idx_movies_metadata_backfill_pending
    on movies (id)
    where tmdb_id > 0 and metadata_refresh_status is distinct from 'success';

create index if not exists idx_tv_shows_metadata_backfill_pending
    on tv_shows (id)
    where tmdb_id > 0 and metadata_refresh_status is distinct from 'success';
