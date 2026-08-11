alter table virtual_files add column if not exists rar_encryption_salt bytea;
alter table virtual_files add column if not exists rar_encryption_iv bytea;
alter table virtual_files add column if not exists rar_encryption_lg2_count smallint;
