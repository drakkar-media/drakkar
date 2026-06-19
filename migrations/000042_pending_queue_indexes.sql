-- Speed pending search dispatch and missing-episode backfill for large libraries.
CREATE INDEX IF NOT EXISTS idx_queue_items_pending_dispatch
    ON queue_items (state, updated_at, created_at, library_item_id)
    WHERE state IN ('requested', 'failed', 'selected');

CREATE INDEX IF NOT EXISTS idx_library_items_pending_search
    ON library_items (id, media_type, episode_id, movie_id)
    WHERE available = false
      AND media_type IN ('movie', 'episode', 'tv');

CREATE INDEX IF NOT EXISTS idx_episodes_tv_show_air_date
    ON episodes (tv_show_id, air_date, season_number, episode_number);

CREATE INDEX IF NOT EXISTS idx_tv_shows_tmdb_episode_totals
    ON tv_shows (tmdb_id, number_of_episodes, id)
    WHERE tmdb_id > 0
      AND number_of_episodes > 0;
