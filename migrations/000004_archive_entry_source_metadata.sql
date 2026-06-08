alter table archive_entries
    add column if not exists packed_size_bytes bigint not null default 0,
    add column if not exists source_volume_index integer not null default 0,
    add column if not exists source_archive_offset bigint not null default 0;
