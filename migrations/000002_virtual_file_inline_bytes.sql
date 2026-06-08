alter table virtual_files
    add column if not exists inline_bytes bytea;
