create table if not exists media_cleanup_jobs (
    id bigserial primary key,
    media_type text not null,
    tmdb_id bigint not null default 0,
    title text not null default '',
    external_request_ids text[] not null default '{}',
    library_paths text[] not null default '{}',
    subtitle_paths text[] not null default '{}',
    attempts integer not null default 0,
    last_error text not null default '',
    next_attempt_at timestamptz not null default now(),
    created_at timestamptz not null default now(),
    completed_at timestamptz
);

create index if not exists idx_media_cleanup_jobs_pending
    on media_cleanup_jobs (next_attempt_at, id)
    where completed_at is null;

create index if not exists idx_media_cleanup_jobs_pending_media
    on media_cleanup_jobs (media_type, tmdb_id)
    where completed_at is null;

create index if not exists idx_media_cleanup_jobs_external_requests
    on media_cleanup_jobs using gin (external_request_ids);
