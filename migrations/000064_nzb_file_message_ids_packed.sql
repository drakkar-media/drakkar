alter table nzb_files
    add column if not exists message_ids_packed bytea,
    add column if not exists message_id_count integer not null default 0;

update nzb_files
set message_id_count = coalesce(array_length(message_ids, 1), 0)
where message_id_count = 0
  and coalesce(array_length(message_ids, 1), 0) > 0;
