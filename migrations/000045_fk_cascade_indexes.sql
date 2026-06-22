-- Add missing FK indexes to eliminate cascade-delete lock contention.
-- Without these, deleting release_candidates triggers sequential scans on
-- grab_history and selected_releases (~600ms + ~340ms per row), causing
-- "driver: bad connection" errors under concurrent worker load.

create index if not exists idx_grab_history_release_candidate_id
    on grab_history(release_candidate_id);

create index if not exists idx_selected_releases_release_candidate_id
    on selected_releases(release_candidate_id);

create index if not exists idx_archive_entries_archive_id
    on archive_entries(archive_id);

create index if not exists idx_archive_volumes_archive_id
    on archive_volumes(archive_id);

create index if not exists idx_archive_ranges_archive_entry_id
    on archive_ranges(archive_entry_id);

create index if not exists idx_archive_ranges_archive_volume_id
    on archive_ranges(archive_volume_id);

create index if not exists idx_virtual_files_nzb_file_id
    on virtual_files(nzb_file_id);

create index if not exists idx_symlink_publications_virtual_file_id
    on symlink_publications(virtual_file_id);

create index if not exists idx_library_items_episode_id
    on library_items(episode_id);

create index if not exists idx_library_items_movie_id
    on library_items(movie_id);

create index if not exists idx_library_items_quality_profile_id
    on library_items(quality_profile_id);

create index if not exists idx_subtitle_candidates_library_item_id
    on subtitle_candidates(library_item_id);

create index if not exists idx_subtitle_files_library_item_id
    on subtitle_files(library_item_id);
