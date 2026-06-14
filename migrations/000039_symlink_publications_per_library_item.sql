-- Allow multiple library items to reference the same physical symlink path.
-- Season packs and their individual episode items both need their own
-- symlink_publications row so each shows as "available" in health checks.
alter table symlink_publications drop constraint symlink_publications_library_path_key;
alter table symlink_publications add constraint symlink_publications_library_item_path_key unique (library_item_id, library_path);
