-- Ensure one library item per episode so season-pack fulfillment can
-- upsert per-episode records without creating duplicates.
CREATE UNIQUE INDEX IF NOT EXISTS library_items_episode_id_unique
    ON library_items (episode_id)
    WHERE episode_id IS NOT NULL;
