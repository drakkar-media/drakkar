alter table symlink_publications
    add column if not exists last_checked_at timestamptz,
    add column if not exists health_ok boolean;
