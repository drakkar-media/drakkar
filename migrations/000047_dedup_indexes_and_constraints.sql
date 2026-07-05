-- Drop redundant indexes that duplicate an existing unique constraint's
-- implicit index on the exact same leading column(s) — wasted disk/write cost.
drop index if exists public.episodes_tv_show_id_s_e;
drop index if exists public.idx_season_pack_attempts_show_season;
drop index if exists public.seasons_tv_show_id_idx;

-- tv_shows had no unique constraint on tmdb_id, unlike movies (movies_tmdb_id_key).
-- App code (UpsertEpisodeRequest) does check-then-insert assuming tmdb_id
-- uniqueness per show; without a DB constraint a race could create duplicate
-- tv_shows rows for the same TMDB id.
alter table public.tv_shows
    add constraint tv_shows_tmdb_id_key unique (tmdb_id);

-- library_items had a partial unique index on episode_id but nothing
-- equivalent for movie_id, so movie library items relied entirely on
-- app-level check-then-insert with no DB backstop against duplicates.
create unique index if not exists library_items_movie_id_unique
    on public.library_items (movie_id)
    where movie_id is not null;
