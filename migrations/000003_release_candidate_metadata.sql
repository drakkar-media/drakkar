alter table release_candidates
    add column if not exists external_url text not null default '',
    add column if not exists indexer_name text not null default '',
    add column if not exists size_bytes bigint not null default 0,
    add column if not exists posted_at timestamptz;
