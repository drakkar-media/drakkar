alter table subtitle_candidates
    add column if not exists title text not null default '',
    add column if not exists release_name text not null default '',
    add column if not exists format text not null default '',
    add column if not exists hearing_impaired boolean not null default false,
    add column if not exists download_url text not null default '';
