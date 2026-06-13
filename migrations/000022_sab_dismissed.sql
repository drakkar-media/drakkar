-- Mark queue items as dismissed from the SABnzbd-compatible history/queue view.
-- Radarr/Sonarr send mode=history&name=delete after importing; dismissed items
-- are excluded from future history and queue polls without altering queue state.
ALTER TABLE queue_items ADD COLUMN IF NOT EXISTS sab_dismissed boolean NOT NULL DEFAULT false;
