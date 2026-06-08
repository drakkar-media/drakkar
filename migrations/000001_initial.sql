create table if not exists media_requests (
    id bigserial primary key,
    external_id text,
    request_type text not null,
    created_at timestamptz not null default now()
);

create table if not exists movies (
    id bigserial primary key,
    tmdb_id bigint unique,
    imdb_id text,
    title text not null,
    release_year integer
);

create table if not exists tv_shows (
    id bigserial primary key,
    tvdb_id bigint unique,
    tmdb_id bigint,
    imdb_id text,
    title text not null,
    release_year integer
);

create table if not exists seasons (
    id bigserial primary key,
    tv_show_id bigint not null references tv_shows(id) on delete cascade,
    season_number integer not null,
    unique (tv_show_id, season_number)
);

create table if not exists episodes (
    id bigserial primary key,
    tv_show_id bigint not null references tv_shows(id) on delete cascade,
    season_number integer not null,
    episode_number integer not null,
    tvdb_id bigint,
    tmdb_id bigint,
    title text,
    unique (tv_show_id, season_number, episode_number)
);

create table if not exists library_items (
    id bigserial primary key,
    media_type text not null,
    movie_id bigint references movies(id) on delete cascade,
    episode_id bigint references episodes(id) on delete cascade,
    title text not null,
    requested_at timestamptz not null default now(),
    available boolean not null default false
);

create table if not exists release_candidates (
    id bigserial primary key,
    library_item_id bigint not null references library_items(id) on delete cascade,
    title text not null,
    score integer not null default 0,
    selected boolean not null default false,
    rejected boolean not null default false,
    reject_reason text not null default '',
    failure_count integer not null default 0,
    last_failure_reason text not null default '',
    created_at timestamptz not null default now()
);

create table if not exists selected_releases (
    id bigserial primary key,
    library_item_id bigint not null references library_items(id) on delete cascade,
    release_candidate_id bigint not null references release_candidates(id) on delete cascade,
    created_at timestamptz not null default now()
);

create table if not exists nzb_documents (
    id bigserial primary key,
    selected_release_id bigint not null references selected_releases(id) on delete cascade,
    external_url text,
    file_name text not null,
    xml bytea,
    created_at timestamptz not null default now()
);

create table if not exists nzb_files (
    id bigserial primary key,
    nzb_document_id bigint not null references nzb_documents(id) on delete cascade,
    subject text not null,
    poster text,
    posted_at timestamptz,
    file_size_bytes bigint not null default 0
);

create table if not exists nzb_segments (
    id bigserial primary key,
    nzb_file_id bigint not null references nzb_files(id) on delete cascade,
    segment_number integer not null,
    message_id text not null,
    encoded_size_bytes bigint not null default 0,
    decoded_start_offset bigint not null default 0,
    decoded_end_offset bigint not null default 0,
    availability_status text not null default 'unknown',
    unique (nzb_file_id, segment_number)
);

create table if not exists virtual_files (
    id bigserial primary key,
    selected_release_id bigint not null references selected_releases(id) on delete cascade,
    path text not null,
    file_name text not null,
    size_bytes bigint not null default 0,
    reader_kind text not null,
    unique (selected_release_id, path)
);

create table if not exists virtual_file_ranges (
    id bigserial primary key,
    virtual_file_id bigint not null references virtual_files(id) on delete cascade,
    nzb_segment_id bigint not null references nzb_segments(id) on delete cascade,
    range_start bigint not null,
    range_end bigint not null
);

create table if not exists archives (
    id bigserial primary key,
    selected_release_id bigint not null references selected_releases(id) on delete cascade,
    kind text not null,
    status text not null default 'pending',
    reject_reason text not null default ''
);

create table if not exists archive_volumes (
    id bigserial primary key,
    archive_id bigint not null references archives(id) on delete cascade,
    path text not null,
    volume_index integer not null
);

create table if not exists archive_entries (
    id bigserial primary key,
    archive_id bigint not null references archives(id) on delete cascade,
    path text not null,
    size_bytes bigint not null default 0,
    compression_method text not null default '',
    encrypted boolean not null default false,
    solid boolean not null default false
);

create table if not exists archive_ranges (
    id bigserial primary key,
    archive_entry_id bigint not null references archive_entries(id) on delete cascade,
    archive_volume_id bigint not null references archive_volumes(id) on delete cascade,
    entry_offset bigint not null,
    archive_offset bigint not null,
    length_bytes bigint not null
);

create table if not exists stream_sessions (
    id bigserial primary key,
    virtual_file_id bigint not null references virtual_files(id) on delete cascade,
    state text not null,
    offset_bytes bigint not null default 0,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table if not exists failed_releases (
    id bigserial primary key,
    release_candidate_id bigint not null references release_candidates(id) on delete cascade,
    reason text not null,
    created_at timestamptz not null default now()
);

create table if not exists blocklist_items (
    id bigserial primary key,
    key text not null unique,
    reason text not null,
    expires_at timestamptz
);

create table if not exists queue_items (
    id bigserial primary key,
    library_item_id bigint not null references library_items(id) on delete cascade,
    state text not null,
    failure_reason text not null default '',
    idempotency_key text not null unique,
    selected_release_id bigint references selected_releases(id) on delete set null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table if not exists symlink_publications (
    id bigserial primary key,
    library_item_id bigint not null references library_items(id) on delete cascade,
    virtual_file_id bigint not null references virtual_files(id) on delete cascade,
    library_path text not null unique,
    target_path text not null,
    created_at timestamptz not null default now()
);

create table if not exists subtitle_files (
    id bigserial primary key,
    library_item_id bigint not null references library_items(id) on delete cascade,
    provider text not null,
    language text not null,
    path text not null,
    created_at timestamptz not null default now()
);

create table if not exists subtitle_candidates (
    id bigserial primary key,
    library_item_id bigint not null references library_items(id) on delete cascade,
    provider text not null,
    language text not null,
    score integer not null default 0,
    external_id text not null,
    created_at timestamptz not null default now()
);

create table if not exists maintenance_cursors (
    id bigserial primary key,
    task_name text not null unique,
    cursor text not null default '',
    updated_at timestamptz not null default now()
);
